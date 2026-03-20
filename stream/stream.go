// Package stream provides a reactive SSE stream backed by pub/sub messaging.
//
// Components register scopes during render via [WatchEffect]. The stream
// handler subscribes to pub/sub topics for those scopes and pushes stale
// signals when invalidations occur. Components watch the stale signal
// via data-effect and reload themselves.
//
// Scopes use colon-separated naming: "invoice:42", "invoices:*", "workspace:1:*".
// Wildcards: * = single level, > = rest (NATS convention, supported by all adapters).
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
	"github.com/laenen-partners/dsx/pubsub"
	"github.com/starfederation/datastar-go/datastar"
)

const (
	// SignalNamespace is the Datastar signal namespace for stream stale flags.
	// The underscore prefix makes it local-only (never sent to backend).
	SignalNamespace = "_stream"

	// DataNamespace is the Datastar signal namespace for scope payload data.
	// When InvalidateWithData is used, entity data is pushed under this namespace.
	DataNamespace = "_streamData"

	defaultSubjectPrefix = "dsx.scope"

	// maxScopes is the maximum number of scopes a single SSE connection may
	// subscribe to. This prevents a malicious client from exhausting memory
	// by requesting an unbounded number of scope subscriptions.
	maxScopes = 64
)

// staleMsg carries scope signal keys and optional JSON payload through the event channel.
type staleMsg struct {
	keys []string
	data []byte // nil for plain invalidations
}

// controlMsg is the JSON structure sent over the NATS control channel
// for dynamic scope management on a live SSE connection.
type controlMsg struct {
	Action string `json:"action"`
	Scope  string `json:"scope"`
}

// Broker wraps a PubSub backend and provides publish/subscribe for scope
// invalidation. One Broker per application.
type Broker struct {
	ps              pubsub.PubSub
	prefix          string
	maxConnDuration time.Duration
}

// Option configures the Broker.
type Option func(*Broker)

// WithSubjectPrefix overrides the default topic prefix ("dsx.scope").
func WithSubjectPrefix(prefix string) Option {
	return func(b *Broker) { b.prefix = prefix }
}

// WithMaxConnectionDuration sets a maximum lifetime for SSE connections.
// When the duration elapses the handler returns, causing Datastar to
// reconnect and re-run any auth middleware.
func WithMaxConnectionDuration(d time.Duration) Option {
	return func(b *Broker) { b.maxConnDuration = d }
}

// NewBroker creates a Broker from any PubSub backend.
//
//	broker := stream.NewBroker(natspubsub.New(nc))
//	broker := stream.NewBroker(chanpubsub.New())
func NewBroker(ps pubsub.PubSub, opts ...Option) *Broker {
	b := &Broker{ps: ps, prefix: defaultSubjectPrefix}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Invalidate publishes an invalidation message for the given scope.
// Call this after mutations to notify all connected browsers.
//
//	broker.Invalidate("invoice:42")
func (b *Broker) Invalidate(scope string) error {
	subject := scopeToSubject(b.prefix, scope)
	if err := b.ps.Publish(subject, nil); err != nil {
		return fmt.Errorf("publishing invalidation for %q: %w", scope, err)
	}
	return nil
}

// InvalidateWithData publishes an invalidation with an attached data payload.
// The data is JSON-encoded and pushed to clients under the DataNamespace
// alongside the stale flag.
//
//	broker.InvalidateWithData("invoice:42", invoice)
func (b *Broker) InvalidateWithData(scope string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling data for %q: %w", scope, err)
	}
	subject := scopeToSubject(b.prefix, scope)
	if err := b.ps.Publish(subject, payload); err != nil {
		return fmt.Errorf("publishing invalidation with data for %q: %w", scope, err)
	}
	return nil
}

// InvalidateMany publishes invalidation for multiple scopes at once.
func (b *Broker) InvalidateMany(scopes ...string) error {
	for _, scope := range scopes {
		if err := b.Invalidate(scope); err != nil {
			return err
		}
	}
	return nil
}

// AddScope publishes a control message to add a NATS subscription to a live
// SSE connection identified by sessionID.
func (b *Broker) AddScope(sessionID, scope string) error {
	msg := controlMsg{Action: "subscribe", Scope: scope}
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling control message: %w", err)
	}
	subject := b.controlSubject(sessionID)
	if err := b.ps.Publish(subject, payload); err != nil {
		return fmt.Errorf("publishing control message for session %q: %w", sessionID, err)
	}
	return nil
}

// SubscribeHandler returns an http.HandlerFunc that accepts POST requests
// to dynamically add scopes to a live SSE connection. The request must
// include "scope" form value and the session must have an active stream.
func (b *Broker) SubscribeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		scope := r.FormValue("scope")
		if scope == "" {
			http.Error(w, "missing scope parameter", http.StatusBadRequest)
			return
		}

		wxctx := dsx.FromContext(r.Context())
		if wxctx.SessionID == "" {
			http.Error(w, "no session", http.StatusBadRequest)
			return
		}

		if err := b.AddScope(wxctx.SessionID, scope); err != nil {
			slog.Error("stream: add scope failed", "session", wxctx.SessionID, "scope", scope, "error", err)
			http.Error(w, "failed to add scope", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// controlSubject returns the topic for the control channel of a session.
func (b *Broker) controlSubject(sessionID string) string {
	return b.prefix + ".ctrl." + sessionID
}

// Handler returns an http.HandlerFunc that serves the persistent SSE stream.
// It reads scopes from the query parameters (comma-separated or grouped by entity)
// and subscribes to pub/sub topics for each scope (supporting exact and wildcard
// patterns). It also listens on a per-session control channel for dynamic scope
// additions.
//
// When a "keys" query param is present (JSON map of scope→[]signalKey), the handler
// pushes all signal keys for a matching scope. This supports multiple components
// watching the same scope with independent signals.
func (b *Broker) Handler() http.HandlerFunc {
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
		if b.maxConnDuration > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, b.maxConnDuration)
			defer cancel()
		}

		staleC := make(chan staleMsg, 64)

		var mu sync.Mutex
		var subs []pubsub.Subscription
		subscribed := make(map[string]bool)

		// addScope subscribes to a scope's topic. Thread-safe.
		addScope := func(scope string, keys []string) {
			mu.Lock()
			defer mu.Unlock()

			subject := scopeToSubject(b.prefix, scope)
			if subscribed[subject] {
				return
			}
			if len(subscribed) >= maxScopes {
				slog.Warn("stream: max scopes reached, ignoring", "scope", scope)
				return
			}

			sub, err := b.ps.Subscribe(subject, func(data []byte) {
				sm := staleMsg{keys: keys, data: data}
				select {
				case staleC <- sm:
				default:
				}
			})
			if err != nil {
				slog.Error("stream: subscribe failed", "scope", scope, "subject", subject, "error", err)
				return
			}
			subs = append(subs, sub)
			subscribed[subject] = true
		}

		// Subscribe to initial scopes.
		for _, scope := range scopes {
			addScope(scope, scopeKeys[scope])
		}

		// Subscribe to session control channel for dynamic scope additions.
		wxctx := dsx.FromContext(r.Context())
		if wxctx.SessionID != "" {
			ctrlSubject := b.controlSubject(wxctx.SessionID)
			ctrlSub, err := b.ps.Subscribe(ctrlSubject, func(data []byte) {
				var ctrl controlMsg
				if err := json.Unmarshal(data, &ctrl); err != nil {
					slog.Error("stream: bad control message", "error", err)
					return
				}
				if ctrl.Action == "subscribe" && ctrl.Scope != "" {
					keys := []string{ScopeKey(ctrl.Scope)}
					addScope(ctrl.Scope, keys)
					// Push init signal so the client knows about the new scope.
					sm := staleMsg{keys: keys}
					select {
					case staleC <- sm:
					default:
					}
				}
			})
			if err != nil {
				slog.Error("stream: control channel subscribe failed", "subject", ctrlSubject, "error", err)
			} else {
				mu.Lock()
				subs = append(subs, ctrlSub)
				mu.Unlock()
			}
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

// scopeToSubject converts a scope string to a pub/sub topic.
// Colons become dots, * and > stay as wildcards.
//
//	"invoice:42"      → "dsx.scope.invoice.42"
//	"invoices:*"      → "dsx.scope.invoices.*"
//	"workspace:1:*"   → "dsx.scope.workspace.1.*"
func scopeToSubject(prefix, scope string) string {
	safe := strings.ReplaceAll(scope, ":", ".")
	return prefix + "." + safe
}
