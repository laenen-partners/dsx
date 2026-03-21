// Package stream provides DOM-driven SSE subscriptions backed by pub/sub.
//
// Components declare subscriptions via data-watch attributes on their DOM
// elements. A MutationObserver-based JS worker tracks these attributes and
// manages SSE reconnects. The server pushes structured _dsEvent signals with
// {domain, id, action, ts}. Components react via data-effect with
// action-aware conditions.
//
// The Watch function returns templ.Attributes that wire up a subscription
// and data-effect expressions for action-aware reloading:
//
//	stream.Watch(ctx, "customers",
//	    stream.Reload("created,deleted", "/api/customers/list"))
//	stream.Watch(ctx, "customers",
//	    stream.Reload("updated", "/api/row/42", stream.WithID(42)))
//
// Publishing is the app's responsibility via [pubsub.Bus] methods like
// NotifyCreated, NotifyUpdated, etc.
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/laenen-partners/identity"
	"github.com/laenen-partners/pubsub"
	"github.com/starfederation/datastar-go/datastar"
)

const (
	// EventSignal is the Datastar signal name used for structured change events.
	EventSignal = "_dsEvent"

	// maxWatches is the maximum number of watch subscriptions a single SSE
	// connection may have. This prevents resource exhaustion.
	maxWatches = 64
)

// Reaction describes what should happen when a matching change event arrives.
type Reaction struct {
	actions string // comma-separated actions (e.g. "created,deleted") or "*"
	url     string // URL to fetch when triggered
	id      string // optional: filter to specific entity ID
}

// ReloadOption configures a Reload reaction.
type ReloadOption func(*Reaction)

// WithID filters the reaction to a specific entity ID.
func WithID(id any) ReloadOption {
	return func(r *Reaction) {
		r.id = fmt.Sprintf("%v", id)
	}
}

// Reload creates a reaction that fetches a URL when matching actions arrive.
//
//	stream.Reload("created,deleted", wxctx.APIPath("/customers/list"))
//	stream.Reload("updated", wxctx.APIPath("/customers/42/row"), stream.WithID(42))
//	stream.Reload("*", wxctx.APIPath("/customers/count"))
func Reload(actions string, url string, opts ...ReloadOption) Reaction {
	r := Reaction{actions: actions, url: url}
	for _, opt := range opts {
		opt(&r)
	}
	return r
}

// Watch returns templ.Attributes that wire up a subscription element and
// data-effect expressions for action-aware reloading.
//
//	stream.Watch(ctx, "customers",
//	    stream.Reload("created,deleted", wxctx.APIPath("/customers/list")))
//	stream.Watch(ctx, "customers",
//	    stream.Reload("updated", wxctx.APIPath("/customers/42/row"), stream.WithID(42)))
func Watch(_ context.Context, domain string, reactions ...Reaction) templ.Attributes {
	attrs := templ.Attributes{}

	// Determine if any reaction has an ID filter — use domain.id for watch value.
	// If multiple reactions have different IDs, we use just the domain (broad watch).
	watchValue := domain
	var singleID string
	for _, r := range reactions {
		if r.id != "" {
			if singleID == "" {
				singleID = r.id
			} else if singleID != r.id {
				singleID = ""
				break
			}
		}
	}
	if singleID != "" {
		watchValue = domain + "." + singleID
	}

	attrs["data-watch"] = watchValue

	// Build data-effect expression(s) from reactions.
	var effects []string
	for _, r := range reactions {
		effects = append(effects, buildEffect(domain, r))
	}

	if len(effects) > 0 {
		attrs["data-effect"] = strings.Join(effects, " ")
	}

	return attrs
}

// buildEffect generates a data-effect expression for a single reaction.
// The expression references $_dsEvent.ts to ensure Datastar detects every
// signal change (even if domain/id/action are repeated).
func buildEffect(domain string, r Reaction) string {
	// Base condition: check that the event domain matches and ts > 0
	// (ts > 0 prevents the initial zero-value from triggering).
	var conditions []string
	conditions = append(conditions, fmt.Sprintf("$%s.ts > 0", EventSignal))
	conditions = append(conditions, fmt.Sprintf("$%s.domain === '%s'", EventSignal, domain))

	// Action filter.
	if r.actions != "*" {
		actions := strings.Split(r.actions, ",")
		if len(actions) == 1 {
			conditions = append(conditions, fmt.Sprintf("$%s.action === '%s'", EventSignal, strings.TrimSpace(actions[0])))
		} else {
			var parts []string
			for _, a := range actions {
				parts = append(parts, fmt.Sprintf("'%s'", strings.TrimSpace(a)))
			}
			conditions = append(conditions, fmt.Sprintf("[%s].includes($%s.action)", strings.Join(parts, ","), EventSignal))
		}
	}

	// ID filter.
	if r.id != "" {
		conditions = append(conditions, fmt.Sprintf("$%s.id === '%s'", EventSignal, r.id))
	}

	condition := strings.Join(conditions, " && ")
	return fmt.Sprintf("if(%s) { @get('%s') }", condition, r.url)
}

// EventSignals returns the initial data-signals value for _dsEvent.
func EventSignals() string {
	return fmt.Sprintf("{%s: {domain: '', id: '', action: '', ts: 0}}", EventSignal)
}

// Relay listens for pub/sub change notifications and relays them to SSE
// clients as structured event signals. One Relay per application.
//
// Publishing is the app's responsibility via [pubsub.Bus]:
//
//	bus.NotifyUpdated(ctx, "customer", "42")
type Relay struct {
	ps              pubsub.PubSub
	maxConnDuration time.Duration
}

// Option configures the Relay.
type Option func(*Relay)

// WithMaxConnectionDuration sets a maximum lifetime for SSE connections.
// When the duration elapses the handler returns, causing Datastar to
// reconnect and re-run any auth middleware.
func WithMaxConnectionDuration(d time.Duration) Option {
	return func(r *Relay) { r.maxConnDuration = d }
}

// New creates a Relay from any PubSub backend.
//
//	relay := stream.New(chanpubsub.New())
//	relay := stream.New(natspubsub.New(nc))
func New(ps pubsub.PubSub, opts ...Option) *Relay {
	r := &Relay{ps: ps}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// eventMsg carries a structured change event through the internal channel.
type eventMsg struct {
	Domain string `json:"domain"`
	ID     string `json:"id"`
	Action string `json:"action"`
	TS     int64  `json:"ts"`
}

// Handler returns an http.HandlerFunc that serves the persistent SSE stream.
// It reads watch subscriptions from the "watch" query parameter (comma-separated)
// and subscribes to pub/sub change topics for each.
//
// Watch values use dot-separated format:
//
//	"doc"     → subscribes to all changes for entity "doc"
//	"doc.123" → subscribes to changes for entity "doc", id "123"
//
// On notification, pushes a structured _dsEvent signal:
//
//	{"_dsEvent": {"domain": "doc", "id": "123", "action": "updated", "ts": 1234567890}}
func (rl *Relay) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		watches := parseWatches(r)
		if len(watches) == 0 {
			return
		}
		if len(watches) > maxWatches {
			http.Error(w, fmt.Sprintf("too many watches (max %d)", maxWatches), http.StatusBadRequest)
			return
		}

		sse := datastar.NewSSE(w, r)
		ctx := r.Context()
		if rl.maxConnDuration > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, rl.maxConnDuration)
			defer cancel()
		}

		eventC := make(chan eventMsg, 64)

		var mu sync.Mutex
		var subs []pubsub.Subscription

		for _, watch := range watches {
			pattern := watchToPattern(r.Context(), watch)

			sub, err := rl.ps.Subscribe(r.Context(), pattern, func(data []byte) {
				// Try to unmarshal the envelope to extract the change notification.
				var env pubsub.Envelope
				if err := json.Unmarshal(data, &env); err != nil {
					slog.Error("stream: unmarshal envelope", "error", err)
					return
				}
				var cn pubsub.ChangeNotification
				if err := json.Unmarshal(env.Data, &cn); err != nil {
					slog.Error("stream: unmarshal change notification", "error", err)
					return
				}
				msg := eventMsg{
					Domain: cn.Entity,
					ID:     cn.EntityID,
					Action: cn.Action,
					TS:     env.Time.UnixMilli(),
				}
				select {
				case eventC <- msg:
				default:
				}
			})
			if err != nil {
				slog.Error("stream: subscribe failed", "watch", watch, "pattern", pattern, "error", err)
				continue
			}
			mu.Lock()
			subs = append(subs, sub)
			mu.Unlock()
		}

		defer func() {
			mu.Lock()
			defer mu.Unlock()
			for _, sub := range subs {
				_ = sub.Unsubscribe()
			}
		}()

		// Event loop: wait for pub/sub messages or client disconnect.
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-eventC:
				signals := map[string]any{
					EventSignal: map[string]any{
						"domain": msg.Domain,
						"id":     msg.ID,
						"action": msg.Action,
						"ts":     msg.TS,
					},
				}
				if err := sse.MarshalAndPatchSignals(signals); err != nil {
					return
				}
			}
		}
	}
}

// watchToPattern converts a watch string to a pub/sub change pattern,
// incorporating tenant/workspace from the identity context.
//
// Watch format: "domain" or "domain.id"
//
//	"doc"     → ChangePattern(tenant, workspace, "doc", ">", "")
//	"doc.123" → ChangePattern(tenant, workspace, "doc", "123", ">")
func watchToPattern(ctx context.Context, watch string) string {
	tenant, workspace := "_", "_"
	if id, ok := identity.FromContext(ctx); ok {
		tenant = id.TenantID()
		workspace = id.WorkspaceID()
	}

	domain, entityID, hasID := strings.Cut(watch, ".")
	if !hasID || entityID == "" {
		// Watch all changes for this domain.
		return pubsub.ChangePattern(tenant, workspace, domain, ">", "")
	}
	// Watch specific entity ID, any action.
	return pubsub.ChangePattern(tenant, workspace, domain, entityID, ">")
}

// parseWatches extracts watch values from the "watch" query parameter.
// Values are comma-separated: ?watch=doc,invoice.456
func parseWatches(r *http.Request) []string {
	raw := r.URL.Query().Get("watch")
	if raw == "" {
		return nil
	}
	var watches []string
	for _, w := range strings.Split(raw, ",") {
		w = strings.TrimSpace(w)
		if w != "" {
			watches = append(watches, w)
		}
	}
	return watches
}
