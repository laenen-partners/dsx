// Package stream provides a reactive SSE stream backed by pub/sub messaging.
//
// Components register scopes during render via [WatchEffect]. The stream
// [Relay] subscribes to pub/sub change topics for those scopes and pushes
// stale signals when notifications arrive. Components watch the stale signal
// via data-effect and reload themselves.
//
// Scopes use colon-separated naming that maps to pubsub change topics:
//
//	"customer:42"   → subscribes to change.customer.42.>
//	"customers:*"   → subscribes to change.customers.*.*
//	"customer:>"    → subscribes to change.customer.>
//
// The Relay only subscribes — publishing is the app's responsibility via
// [pubsub.Bus] methods like NotifyCreated, NotifyUpdated, etc.
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
	"github.com/laenen-partners/dsx"
	"github.com/laenen-partners/identity"
	"github.com/laenen-partners/pubsub"
	"github.com/starfederation/datastar-go/datastar"
)

const (
	// SignalNamespace is the Datastar signal namespace for stream stale flags.
	// The underscore prefix makes it local-only (never sent to backend).
	SignalNamespace = "_stream"

	// DataNamespace is the Datastar signal namespace for scope payload data.
	// When data is published alongside a scope notification, it is pushed under this namespace.
	DataNamespace = "_streamData"

	// maxScopes is the maximum number of scopes a single SSE connection may
	// subscribe to. This prevents a malicious client from exhausting memory
	// by requesting an unbounded number of scope subscriptions.
	maxScopes = 64
)

// staleMsg carries scope signal keys and optional JSON payload through the event channel.
type staleMsg struct {
	keys []string
	data []byte // nil for plain notifications
}

// Relay listens for pub/sub change notifications and relays them to SSE
// clients as stale signals. One Relay per application.
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

// Handler returns an http.HandlerFunc that serves the persistent SSE stream.
// It reads scopes from the query parameters (comma-separated or grouped by entity)
// and subscribes to pub/sub change topics for each scope. Topics are
// automatically scoped by tenant/workspace from the request's identity context.
//
// When a "keys" query param is present (JSON map of scope→[]signalKey), the handler
// pushes all signal keys for a matching scope. This supports multiple components
// watching the same scope with independent signals.
func (rl *Relay) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scopes := parseScopes(r)
		if len(scopes) == 0 {
			return
		}
		if len(scopes) > maxScopes {
			http.Error(w, fmt.Sprintf("too many scopes (max %d)", maxScopes), http.StatusBadRequest)
			return
		}

		// Parse the optional key map: scope → list of signal keys.
		// Without it, each scope uses its default ScopeKey.
		scopeKeys := parseScopeKeys(r, scopes)

		sse := datastar.NewSSE(w, r)
		ctx := r.Context()
		if rl.maxConnDuration > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, rl.maxConnDuration)
			defer cancel()
		}

		staleC := make(chan staleMsg, 64)

		var mu sync.Mutex
		var subs []pubsub.Subscription

		// Subscribe to each scope's change topic, scoped by identity.
		for _, scope := range scopes {
			keys := scopeKeys[scope]
			pattern := scopeToPattern(r.Context(), scope)

			sub, err := rl.ps.Subscribe(r.Context(), pattern, func(data []byte) {
				sm := staleMsg{keys: keys, data: data}
				select {
				case staleC <- sm:
				default:
				}
			})
			if err != nil {
				slog.Error("stream: subscribe failed", "scope", scope, "pattern", pattern, "error", err)
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
			case msg := <-staleC:
				staleSignals := make(map[string]any, len(msg.keys))
				for _, k := range msg.keys {
					staleSignals[k] = true
				}
				signals := map[string]any{
					SignalNamespace: staleSignals,
				}
				if len(msg.data) > 0 {
					var payload any
					if err := json.Unmarshal(msg.data, &payload); err == nil {
						dataSignals := make(map[string]any, len(msg.keys))
						for _, k := range msg.keys {
							dataSignals[k] = payload
						}
						signals[DataNamespace] = dataSignals
					}
				}
				if err := sse.MarshalAndPatchSignals(signals); err != nil {
					return
				}
			}
		}
	}
}

// scopeToPattern converts a scope string to a pub/sub change pattern,
// incorporating tenant/workspace from the identity context.
//
// Scope format: "entity:id" or "entity:*" or "entity:>"
//
//	"customer:42"  → ChangePattern(tenant, workspace, "customer", "42", ">")
//	"customers:*"  → ChangePattern(tenant, workspace, "customers", "*", "*")
//	"customer:>"   → ChangePattern(tenant, workspace, "customer", ">", "")
func scopeToPattern(ctx context.Context, scope string) string {
	tenant, workspace := "_", "_"
	if id, ok := identity.FromContext(ctx); ok {
		tenant = id.TenantID()
		workspace = id.WorkspaceID()
	}

	entity, entityID, _ := strings.Cut(scope, ":")
	if entityID == "" {
		entityID = ">"
	}

	// Map scope wildcards to ChangePattern conventions.
	switch entityID {
	case ">":
		// Match everything under this entity.
		return pubsub.ChangePattern(tenant, workspace, entity, ">", "")
	case "*":
		// Match any single entity ID, any action.
		return pubsub.ChangePattern(tenant, workspace, entity, "*", "*")
	default:
		// Match a specific entity ID, any action.
		return pubsub.ChangePattern(tenant, workspace, entity, entityID, ">")
	}
}

// parseScopeKeys extracts the scope→keys mapping from the "keys" query param.
// Falls back to ScopeKey(scope) for each scope when no key map is present.
func parseScopeKeys(r *http.Request, scopes []string) map[string][]string {
	result := make(map[string][]string, len(scopes))

	if keysJSON := r.URL.Query().Get("keys"); keysJSON != "" {
		var keyMap map[string][]string
		if err := json.Unmarshal([]byte(keysJSON), &keyMap); err == nil {
			return keyMap
		}
	}

	// Default: one key per scope.
	for _, scope := range scopes {
		result[scope] = []string{ScopeKey(scope)}
	}
	return result
}

// parseScopes extracts scopes from the request query string.
// It supports three formats that can be mixed:
//
//	?scope=invoice:42,invoices:*          (comma-separated)
//	?scope=invoice:42&scope=invoices:*    (repeated params)
//	?customers=1,2,4&files=5,6           (grouped by entity)
//
// The grouped format expands each value into entity:value scopes.
func parseScopes(r *http.Request) []string {
	var scopes []string
	for key, values := range r.URL.Query() {
		for _, v := range values {
			for _, p := range strings.Split(v, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if key == "scope" {
					scopes = append(scopes, p)
				} else {
					scopes = append(scopes, key+":"+p)
				}
			}
		}
	}
	return scopes
}

// ScopeKey converts a scope string to a safe signal property name.
// This is the key within the _stream signal namespace.
//
//	ScopeKey("invoice:42")  → "invoice_42"
//	ScopeKey("invoices:*")  → "invoices_WILD"
//	ScopeKey("workspace:1:*") → "workspace_1_WILD"
func ScopeKey(scope string) string {
	s := strings.ReplaceAll(scope, ":", "_")
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.ReplaceAll(s, "*", "WILD")
	return s
}

// WatchEffect registers a scope on the context and returns a data-effect
// expression string that auto-reloads when the scope goes stale.
//
//	stream.WatchEffect(ctx, "invoice:42", "/showcase/api/invoice/42")
//	// registers scope, returns: "if($_stream.invoice_42) { $_stream.invoice_42 = false; @get('/showcase/api/invoice/42') }"
//
// Multiple components can watch the same scope — each gets a unique signal key.
func WatchEffect(ctx context.Context, scope string, reloadURL string) string {
	wxctx := dsx.FromContext(ctx)
	baseKey := ScopeKey(scope)
	key := wxctx.WatchScope(scope, baseKey)

	signal := fmt.Sprintf("$%s.%s", SignalNamespace, key)
	return fmt.Sprintf("if(%s) { %s = false; @get('%s') }", signal, signal, reloadURL)
}

// Attrs registers a scope and returns templ.Attributes that set up both
// data-signals (stale flag initialization) and data-effect (auto-reload on
// stale). Place these on the component's wrapper element.
//
//	<div { stream.Attrs(ctx, "invoice:42", wxctx.APIPath("/invoice/42"))... }>
func Attrs(ctx context.Context, scope string, reloadURL string) templ.Attributes {
	wxctx := dsx.FromContext(ctx)
	baseKey := ScopeKey(scope)
	key := wxctx.WatchScope(scope, baseKey)

	signal := fmt.Sprintf("$%s.%s", SignalNamespace, key)
	effect := fmt.Sprintf("if(%s) { %s = false; @get('%s') }", signal, signal, reloadURL)

	m := map[string]any{key: false}
	j, _ := json.Marshal(m)
	signals := "{" + SignalNamespace + ": " + string(j) + "}"

	return templ.Attributes{
		"data-signals": signals,
		"data-effect":  effect,
	}
}

// InitScope pushes a PatchSignals to initialize a stale signal for a scope
// that wasn't known at initial render (e.g. new rows from infinite scroll).
func InitScope(sse *datastar.ServerSentEventGenerator, scope string) error {
	key := ScopeKey(scope)
	return sse.MarshalAndPatchSignals(map[string]any{
		SignalNamespace: map[string]any{key: false},
	})
}

// ScopeSignals returns a data-signals value that initializes the stale flags
// for the given scopes. Place this on the component element so the signals
// exist before data-effect runs.
//
//	stream.ScopeSignals("counter:shared") → "_stream: {\"counter_shared\":false}"
func ScopeSignals(scopes ...string) string {
	m := make(map[string]any, len(scopes))
	for _, s := range scopes {
		m[ScopeKey(s)] = false
	}
	j, _ := json.Marshal(m)
	return "{" + SignalNamespace + ": " + string(j) + "}"
}

// PreRegister registers scopes on the dsx context so that stream.Connect()
// opens the SSE stream connection on initial page load. Use this for scopes
// whose fragments are loaded asynchronously (via data-init) and therefore
// can't register themselves in time for the initial render.
//
//	stream.PreRegister(ctx, "counter:shared", "invoices:*")
func PreRegister(ctx context.Context, scopes ...string) {
	wxctx := dsx.FromContext(ctx)
	for _, scope := range scopes {
		wxctx.WatchScope(scope, ScopeKey(scope))
	}
}
