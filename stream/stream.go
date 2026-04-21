// Package stream provides DOM-driven SSE subscriptions backed by pub/sub.
//
// Components declare subscriptions via data-watch attributes on their DOM
// elements. A MutationObserver-based JS worker tracks these attributes and
// manages SSE reconnects. The server pushes per-domain signals (e.g.
// _ds_customers) with {id, action, ts}. Components react via data-effect
// with action-aware conditions.
//
// Each domain gets its own Datastar signal, so an event on "customers" only
// triggers re-evaluation of effects that reference $_ds_customers — not
// unrelated domains. This avoids the O(N) re-evaluation problem of a single
// global signal.
//
// The Watch function returns templ.Attributes that wire up a subscription
// and data-effect expressions for action-aware reloading:
//
//	stream.Watch(ctx, "customers",
//	    stream.Structural.Get("/api/customers/list"),
//	    stream.Any.Get("/api/customers/count"))
//	stream.Watch(ctx, "customers",
//	    stream.Updated.ID(42).Get("/api/row/42"))
//
// The Relay requires a [PatternResolver] that maps watch domains to pub/sub
// subscription patterns. This keeps the stream package agnostic of subject
// format conventions — the app controls scoping and segment ordering.
//
// Publishing is the app's responsibility via its own change notifier.
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
	"github.com/laenen-partners/pubsub"
	"github.com/starfederation/datastar-go/datastar"
)

const (
	// signalPrefix is prepended to the domain name to form the per-domain
	// Datastar signal key (e.g. "_ds_customers").
	signalPrefix = "_ds_"

	// maxWatches is the maximum number of watch subscriptions a single SSE
	// connection may have. This prevents resource exhaustion.
	maxWatches = 64
)

// SignalKey returns the Datastar signal name for a given domain.
func SignalKey(domain string) string {
	return signalPrefix + domain
}

// ActionSet is a set of typed event actions that starts the reaction builder
// chain. Use the predefined sets (Created, Updated, Deleted, Any, Structural)
// or combine them with Or().
//
//	stream.Created.Get(url)
//	stream.Updated.ID(42).Get(url)
//	stream.Structural.Debounce(300*time.Millisecond).Get(url)
//	stream.Created.Or(stream.Deleted).Get(url)
//	stream.Action("archived").Get(url)
type ActionSet struct {
	actions []string
}

// Predefined action sets matching pubsub.Bus notification methods.
var (
	// Created matches "created" events (new entities).
	Created = ActionSet{actions: []string{"created"}}
	// Updated matches "updated" events (modified entities).
	Updated = ActionSet{actions: []string{"updated"}}
	// Deleted matches "deleted" events (removed entities).
	Deleted = ActionSet{actions: []string{"deleted"}}
	// Any matches all actions (wildcard). Use for counters, dashboards, etc.
	Any = ActionSet{actions: []string{"*"}}
	// Structural matches created + deleted — structural changes that affect
	// lists and tables (items added or removed) but not in-place updates.
	Structural = ActionSet{actions: []string{"created", "deleted"}}
)

// Action creates a custom action set for app-specific events.
//
//	stream.Action("archived").Get(url)
//	stream.Action("shipped").Or(stream.Action("delivered")).Get(url)
func Action(name string) ActionSet {
	return ActionSet{actions: []string{name}}
}

// Or combines this action set with another, returning a new set that
// matches any action from either.
//
//	stream.Created.Or(stream.Deleted).Get(url) // same as stream.Structural
//	stream.Updated.Or(stream.Action("archived")).Get(url)
func (a ActionSet) Or(other ActionSet) ActionSet {
	combined := make([]string, 0, len(a.actions)+len(other.actions))
	combined = append(combined, a.actions...)
	combined = append(combined, other.actions...)
	return ActionSet{actions: combined}
}

// ID filters the reaction to a specific entity ID and returns a builder
// that must be finalized with Get().
//
//	stream.Updated.ID(42).Get(url)
func (a ActionSet) ID(id any) *ReactionBuilder {
	return &ReactionBuilder{actions: a.actions, id: fmt.Sprintf("%v", id)}
}

// Debounce adds a debounce delay and returns a builder that must be
// finalized with Get(). When multiple events arrive in rapid succession
// (e.g. bulk creates), only the last one triggers the @get() after the
// delay elapses.
//
//	stream.Structural.Debounce(300*time.Millisecond).Get(url)
func (a ActionSet) Debounce(d time.Duration) *ReactionBuilder {
	return &ReactionBuilder{actions: a.actions, debounce: d}
}

// Get finalizes the reaction with the URL to fetch when triggered.
//
//	stream.Created.Get(url)
//	stream.Any.Get(url)
//	stream.Structural.Get(url)
func (a ActionSet) Get(url string) Reaction {
	return Reaction{actions: a.actions, url: url}
}

// ReactionBuilder is an intermediate builder used when ID() or Debounce()
// is called on an ActionSet. Must be finalized with Get().
type ReactionBuilder struct {
	actions  []string
	id       string
	debounce time.Duration
}

// ID filters the reaction to a specific entity ID.
func (b *ReactionBuilder) ID(id any) *ReactionBuilder {
	b.id = fmt.Sprintf("%v", id)
	return b
}

// Debounce adds a debounce delay to the reaction.
func (b *ReactionBuilder) Debounce(d time.Duration) *ReactionBuilder {
	b.debounce = d
	return b
}

// Get finalizes the reaction with the URL to fetch when triggered.
func (b *ReactionBuilder) Get(url string) Reaction {
	return Reaction{
		actions:  b.actions,
		url:      url,
		id:       b.id,
		debounce: b.debounce,
	}
}

// Reaction describes what should happen when a matching change event arrives.
type Reaction struct {
	actions  []string      // action names (e.g. ["created","deleted"]) or ["*"]
	url      string        // URL to fetch when triggered
	id       string        // optional: filter to specific entity ID
	debounce time.Duration // optional: debounce rapid events
}

// isWildcard returns true if the reaction matches any action.
func (r Reaction) isWildcard() bool {
	for _, a := range r.actions {
		if a == "*" {
			return true
		}
	}
	return false
}

// Watch returns templ.Attributes that wire up a subscription element and
// data-effect expressions for action-aware reloading. Each call adds a
// per-domain data-signals initializer.
//
//	stream.Watch(ctx, "customers",
//	    stream.Structural.Get(wxctx.APIPath("/customers/list")),
//	    stream.Any.Get(wxctx.APIPath("/customers/count")))
//	stream.Watch(ctx, "customers",
//	    stream.Updated.ID(42).Get(wxctx.APIPath("/customers/42/row")))
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

	// Per-domain signal initialization.
	sig := SignalKey(domain)
	attrs["data-signals"] = fmt.Sprintf("{%s: {id: '', action: '', ts: 0}}", sig)

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
//
// Design note: using @get() inside data-effect is an intentional pattern. While
// data-effect is typically used for DOM mutations, Datastar's expression engine
// supports action calls. This lets us express reactive reloads declaratively
// without custom JS event wiring. The trade-off (unconventional usage) is
// accepted because it keeps the API surface minimal and avoids a second
// attribute for the same concern.
//
// The expression references the per-domain signal $_ds_{domain}.ts to ensure
// Datastar detects every signal change. Only effects referencing this domain's
// signal re-evaluate when it changes — other domains are unaffected.
//
// Every effect also matches action === 'connected' so that on SSE reconnect
// the relay's synthetic "connected" event triggers a catch-up reload for all
// watched components.
func buildEffect(domain string, r Reaction) string {
	sig := SignalKey(domain)

	// Base condition: ts > 0 prevents the initial zero-value from triggering.
	var conditions []string
	conditions = append(conditions, fmt.Sprintf("$%s.ts > 0", sig))

	// Action filter. Always include 'connected' for reconnect catch-up.
	if !r.isWildcard() {
		var parts []string
		for _, a := range r.actions {
			parts = append(parts, fmt.Sprintf("'%s'", a))
		}
		parts = append(parts, "'connected'")
		conditions = append(conditions, fmt.Sprintf("[%s].includes($%s.action)", strings.Join(parts, ","), sig))
	}

	// ID filter.
	if r.id != "" {
		conditions = append(conditions, fmt.Sprintf("$%s.id === '%s' || $%s.action === 'connected'", sig, r.id, sig))
	}

	condition := strings.Join(conditions, " && ")

	// Optional debounce wraps @get() in setTimeout/clearTimeout.
	if r.debounce > 0 {
		ms := r.debounce.Milliseconds()
		// Use a stable timer key derived from domain + url to avoid collisions.
		timerKey := fmt.Sprintf("__dsDb_%s_%d", domain, hashString(r.url))
		return fmt.Sprintf("if(%s) { clearTimeout(window.%s); window.%s = setTimeout(() => { @get('%s') }, %d) }",
			condition, timerKey, timerKey, r.url, ms)
	}

	return fmt.Sprintf("if(%s) { @get('%s') }", condition, r.url)
}

// hashString returns a simple hash for generating stable timer keys.
func hashString(s string) uint32 {
	var h uint32
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return h
}

// PatternResolver converts a watch domain (from the browser) and the request
// context into a pub/sub subscription pattern. The app provides this function
// to control subject format, scoping, and segment ordering.
//
// The resolver receives the identity-enriched request context and the raw
// watch value (e.g. "customers", "customers.42"), and must return a pattern
// that matches subjects published by the app's change notifier.
//
//	resolver := func(ctx context.Context, watch string) string {
//	    scope := "platform"
//	    if id, ok := identity.FromContext(ctx); ok && id.TenantID() != "" {
//	        scope = id.TenantID()
//	    }
//	    return scope + ".change." + watch + ".>"
//	}
type PatternResolver func(ctx context.Context, watch string) string

// Relay listens for pub/sub change notifications and relays them to SSE
// clients as per-domain signals. One Relay per application.
//
// Signal delivery is last-event-wins: if two events arrive in rapid succession
// for the same domain, only the latest signal value is visible to Datastar
// effects. This is acceptable because reactions always fetch fresh server state
// via @get() — the signal is a trigger, not the data.
//
// A PatternResolver is required — it defines how watch domains map to pub/sub
// subscription patterns. This keeps dsx agnostic of subject format conventions.
type Relay struct {
	ps              pubsub.PubSub
	resolver        PatternResolver
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

// New creates a Relay from any PubSub backend with an app-provided
// PatternResolver that maps watch domains to subscription patterns.
//
//	relay := stream.New(ps, resolver)
//	relay := stream.New(ps, resolver, stream.WithMaxConnectionDuration(5*time.Minute))
func New(ps pubsub.PubSub, resolver PatternResolver, opts ...Option) *Relay {
	if resolver == nil {
		panic("stream: PatternResolver must not be nil")
	}
	r := &Relay{ps: ps, resolver: resolver}
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
// On notification, pushes a per-domain signal:
//
//	{"_ds_doc": {"id": "123", "action": "updated", "ts": 1234567890}}
//
// On initial connection, a synthetic "connected" event is pushed for each
// watched domain. This allows components to catch up after SSE reconnects —
// every effect includes 'connected' in its action match list.
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

		// Push a "connected" event for each watched domain so components
		// can catch up after SSE reconnects.
		domains := domainsFromWatches(watches)
		now := time.Now().UnixMilli()
		for _, domain := range domains {
			signals := map[string]any{
				SignalKey(domain): map[string]any{
					"id":     "",
					"action": "connected",
					"ts":     now,
				},
			}
			if err := sse.MarshalAndPatchSignals(signals); err != nil {
				return
			}
		}

		eventC := make(chan eventMsg, 64)

		var mu sync.Mutex
		var subs []pubsub.Subscription

		for _, watch := range watches {
			pattern := rl.resolver(r.Context(), watch)

			sub, err := rl.ps.Subscribe(r.Context(), pattern, func(data []byte) {
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
					SignalKey(msg.Domain): map[string]any{
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

// domainsFromWatches extracts unique domain names from watch values.
// "doc.123" → "doc", "counter" → "counter".
func domainsFromWatches(watches []string) []string {
	seen := map[string]struct{}{}
	var domains []string
	for _, w := range watches {
		domain, _, _ := strings.Cut(w, ".")
		if _, ok := seen[domain]; !ok {
			seen[domain] = struct{}{}
			domains = append(domains, domain)
		}
	}
	return domains
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
