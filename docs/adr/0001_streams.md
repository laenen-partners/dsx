# ADR-0001: Streams — Reactive SSE with Pub/Sub

## Status

Accepted

## Context

Server-rendered applications need a way to keep the UI in sync with backend state changes across all connected browsers. Traditional approaches — polling, manual WebSocket wiring, full-page reloads — add complexity and degrade the user experience.

WebX uses Datastar for frontend interactivity. Datastar already supports SSE-driven signal patching and DOM updates. The stream package builds on this to provide a declarative, component-level reactivity model: components declare what data they depend on, and automatically reload when that data changes.

## Decision

### Architecture Overview

The stream package implements a **stale-signal pattern**: components register scopes during render, a persistent SSE connection subscribes to those scopes via pub/sub, and when data changes the server pushes a stale flag that triggers an automatic reload.

```
 Component Render          SSE Connection           Server Mutation
 ────────────────         ────────────────         ─────────────────
 stream.Attrs(ctx,        Connect() opens          bus.NotifyUpdated(ctx,
   "customers:*",         /stream?customers=*        "customers", "42")
   reloadURL)                    │                        │
        │                        │                        │
        ▼                        ▼                        ▼
 Registers watcher        Subscribes to            Publishes to
 on dsx.Context          change topic             change topic
        │                 {tenant}.{workspace}    {tenant}.{workspace}
        │                 .change.customers.*     .change.customers.42.>
        │                        │                        │
        │                        │◄───────────────────────┘
        │                        │
        │                        ▼
        │                 Pushes SSE event:
        │                 {_stream: {customers_WILD: true}}
        │                        │
        ▼                        ▼
 data-effect fires:       Browser receives signal,
 @get(reloadURL)          component reloads itself
```

### Core Concepts

#### Scopes

A scope is a colon-separated string that identifies a piece of reactive data:

| Scope | Meaning |
|-------|---------|
| `"invoice:42"` | Specific invoice with ID 42 |
| `"invoices:*"` | Any single invoice (wildcard) |
| `"customers:*"` | Any customer |
| `"workspace:1:*"` | Anything under workspace 1 |
| `"counter:shared"` | A shared singleton resource |

Scopes map to pub/sub change topics using `pubsub.ChangePattern` conventions:

```
"invoice:42"    → "{tenant}.{workspace}.change.invoice.42.>"
"invoices:*"    → "{tenant}.{workspace}.change.invoices.*"
"workspace:1:*" → "{tenant}.{workspace}.change.workspace.1.*"
```

Wildcards follow NATS conventions:
- `*` matches exactly one segment
- `>` matches one or more remaining segments

#### Signal Keys

Scopes are converted to safe JavaScript signal property names:

```
"invoice:42"      → "invoice_42"
"invoices:*"      → "invoices_WILD"
"workspace:1:*"   → "workspace_1_WILD"
```

These keys live under the `_stream` Datastar signal namespace (underscore prefix = local-only, never sent to backend).

### Component API

#### `stream.Attrs(ctx, scope, reloadURL)`

The primary API. Returns `templ.Attributes` with `data-signals` and `data-effect`:

```go
templ CustomerList() {
    {{ wxctx := dsx.FromContext(ctx) }}
    <div { stream.Attrs(ctx, "customers:*", wxctx.APIPath("/customers/list"))... }>
        <tbody id="customer-table-body"
            { ds.Init(ds.GetOnce(wxctx.APIPath("/customers/list")))... }>
        </tbody>
    </div>
}
```

This generates:

```html
<div data-signals="{_stream: {\"customers_WILD\":false}}"
     data-effect="if($_stream.customers_WILD) { $_stream.customers_WILD = false; @get('/showcase/customers/list') }">
```

The `data-signals` initializes the stale flag to `false`. The `data-effect` watches it — when the stream pushes `true`, the effect fires `@get` to reload the component, then resets the flag.

#### `stream.WatchEffect(ctx, scope, reloadURL)`

Lower-level API that returns just the `data-effect` string. Useful when you need to set `data-signals` separately or customize the element structure:

```go
effect := stream.WatchEffect(ctx, "invoice:42", reloadURL)
// "if($_stream.invoice_42) { $_stream.invoice_42 = false; @get('/showcase/api/invoice/42') }"
```

#### `stream.ScopeSignals(scopes...)`

Returns a `data-signals` value for manual signal initialization:

```go
stream.ScopeSignals("counter:shared")
// "{_stream: {\"counter_shared\":false}}"
```

#### `stream.InitScope(sse, scope)`

Pushes a signal initialization for scopes added after initial render (e.g., infinite scroll new rows):

```go
stream.InitScope(sse, "invoice:99")
// Pushes: {_stream: {invoice_99: false}}
```

### Server API

#### Relay

```go
relay := stream.New(pubsubBackend)
```

The Relay handles SSE connections and subscribes to change topics using `pubsub.ChangePattern` conventions.

#### Bus

Publishing is done through `pubsub.Bus`, which provides semantic change notification methods:

```go
bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope(tenant, workspace))
```

#### Publishing

After a mutation, publish using the `pubsub.Bus` methods:

```go
// Simple publish — triggers reload for all watchers
bus.NotifyUpdated(ctx, "customers", "42")

// Publish to multiple scopes
bus.NotifyUpdated(ctx, "customers", "42")
bus.NotifyUpdated(ctx, "stats", "dashboard")

// Other change types
bus.NotifyCreated(ctx, "invoice", "42")
bus.NotifyDeleted(ctx, "invoice", "42")
```

### Connect Template

`stream.Connect()` is a templ component placed at the end of the page layout (after `{ children... }`) so all component scopes are accumulated before it reads them:

```go
@layouts.Dashboard(...) {
    { children... }
    @stream.Connect()
}
```

It renders a hidden `<div>` that opens the persistent SSE connection:

```html
<div style="display:none"
     data-signals="{_stream: {\"customers_WILD\":false,\"counter_shared\":false}}"
     data-init="@get('/showcase/stream?customers=*&counter=shared', {requestCancellation: 'disabled'})">
</div>
```

The URL uses a **grouped scope format**: scopes with the same entity prefix are collapsed:

```
customers:1, customers:2, files:5  →  ?customers=1,2&files=5
```

Scopes without a colon fall back to `?scope=value`.

### Stream Handler

`relay.Handler()` serves the SSE endpoint. Its lifecycle:

1. **Parse scopes** from query params (grouped or `scope=` format)
2. **Enforce limit** (`maxScopes = 64` per connection)
3. **Subscribe** to pub/sub topic for each scope
4. **Event loop**: receive pub/sub messages → push stale signals via SSE
6. **Cleanup** on client disconnect: unsubscribe all

### Multi-Watcher Design

Multiple components can watch the same scope. Each `stream.Attrs` / `stream.WatchEffect` call generates a unique signal key:

```go
// Component 1: customer list
stream.Attrs(ctx, "customers:*", "/customers/list")
// Key: "customers_WILD"

// Component 2: customer count
stream.Attrs(ctx, "customers:*", "/customers/count")
// Key: "customers_WILD_2"
```

Without unique keys, the first `data-effect` to evaluate would reset the shared signal to `false` before the second could read it.

**How it works internally:**

1. `WatchScope()` maintains a per-key counter. First call returns `customers_WILD`, second returns `customers_WILD_2`, etc.

2. `Connect()` passes a `keys` JSON map in the stream URL when any scope has multiple watchers:
   ```
   ?customers=*&keys={"customers:*":["customers_WILD","customers_WILD_2"]}
   ```

3. The stream handler parses this map and pushes all keys in a single SSE event:
   ```json
   {"_stream": {"customers_WILD": true, "customers_WILD_2": true}}
   ```

4. Each component's `data-effect` independently reads and resets its own key.

When there's only one watcher per scope (the common case), the `keys` param is omitted — zero overhead.

### Pub/Sub Adapters

The relay accepts any `pubsub.PubSub` implementation:

```go
type PubSub interface {
    Publish(topic string, data []byte) error
    Subscribe(topic string, handler func(data []byte)) (Subscription, error)
    Close() error
}

type Subscription interface {
    Unsubscribe() error
}
```

Three adapters are provided:

#### Channel Adapter (`chanpubsub`)

In-process fan-out using Go channels. No external dependencies.

```go
relay := stream.New(chanpubsub.New())
```

- Wildcard matching implemented in Go (`matchTopic` function)
- Handlers called in separate goroutines with data copies
- Suitable for single-process deployments and development

#### NATS Adapter (`natspubsub`)

Thin wrapper around `*nats.Conn`. NATS natively supports `*` and `>` wildcards.

```go
nc, _ := nats.Connect(nats.DefaultURL)
relay := stream.New(natspubsub.New(nc))
```

- Production-grade: distributed, persistent, clustered
- Supports embedded in-process NATS server (used in showcase)
- `Close()` calls `conn.Drain()` for graceful shutdown

#### Redis Adapter (`redispubsub`)

Uses Redis SUBSCRIBE/PSUBSCRIBE with wildcard translation.

```go
rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
relay := stream.New(redispubsub.New(rdb))
```

- Translates `*` → `[^.]*` and `>` → `*` for Redis glob patterns
- Exact topics use SUBSCRIBE, wildcard patterns use PSUBSCRIBE
- Suitable when Redis is already in the stack

### Full Example: Customer List

This example demonstrates the complete pattern — a reactive customer table with a count stat, drawer form, and cross-tab sync.

**Page template** — two components watch `customers:*`:

```go
// Count stat
<div { stream.Attrs(ctx, "customers:*", wxctx.APIPath("/customers/count"))... }>
    <div id="customer-count"
        { ds.Init(ds.GetOnce(wxctx.APIPath("/customers/count")))... }>
        —
    </div>
</div>

// Customer table
<div { stream.Attrs(ctx, "customers:*", wxctx.APIPath("/customers/list"))... }>
    <tbody id="customer-table-body"
        { ds.Init(ds.GetOnce(wxctx.APIPath("/customers/list")))... }>
    </tbody>
</div>

// "Add Customer" button opens drawer
@button.Button(button.Props{
    Attributes: ds.Merge(ds.OnClick(ds.GetOnce(wxctx.APIPath("/customers/new")))),
}) { Add Customer }
```

**Handlers:**

```go
// GET /customers/list — patches table body
func (h *customerHandlers) list() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        h.mu.RLock()
        rows := buildRows(h.customers)
        h.mu.RUnlock()

        sse := datastar.NewSSE(w, r)
        ds.Send.Patch(sse, pages.CustomerTableBody(rows))
    }
}

// GET /customers/count — patches count value
func (h *customerHandlers) count() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        h.mu.RLock()
        n := len(h.customers)
        h.mu.RUnlock()

        sse := datastar.NewSSE(w, r)
        ds.Send.Patch(sse, pages.CustomerCount(n))
    }
}

// GET /customers/new — opens drawer with form
func (h *customerHandlers) newDrawer() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        wxctx := dsx.FromContext(r.Context())
        sse := datastar.NewSSE(w, r)
        ds.Send.Drawer(sse, pages.CustomerDrawer(wxctx.APIPath("/customers/create")))
    }
}

// POST /customers/create — validates, saves, invalidates
func (h *customerHandlers) create() http.HandlerFunc {
    return form.Handler(
        newCustomerSignals{},
        func(formID string, r *http.Request) []form.FieldError {
            var signals newCustomerSignals
            ds.ReadSignals(formID, r, &signals)

            // ... validate ...

            h.mu.Lock()
            id := h.nextID
            h.customers = append(h.customers, customer)
            h.mu.Unlock()

            // This single publish triggers both the table AND the count to reload
            h.bus.NotifyCreated(r.Context(), "customers", strconv.Itoa(id))
            return nil
        },
        func(formID string, sse *datastar.ServerSentEventGenerator) {
            ds.Send.HideDrawer(sse)
            ds.Send.Toast(sse, ds.ToastSuccess, "Customer added successfully")
        },
    )
}
```

**What happens when a customer is added:**

1. Form submits via `@post` → `form.Handler` validates and saves
2. `bus.NotifyCreated(ctx, "customers", "4")` publishes to the change topic
3. The SSE subscription on the matching change pattern receives it
4. Stream pushes `{_stream: {customers_WILD: true, customers_WILD_2: true}}`
5. Count stat's effect fires `@get('/customers/count')` → patches `"4"`
6. Table's effect fires `@get('/customers/list')` → patches updated rows
7. `ds.Send.HideDrawer` closes the form drawer
8. `ds.Send.Toast` shows success notification
9. All of this happens in **every open browser tab** simultaneously

### Connection Lifecycle

#### Page Navigation

Each page renders its own `Connect()` component with its own set of scopes. When the user navigates to a different page, the full lifecycle is:

1. **Browser tears down DOM** — the hidden `<div>` with `data-init` is removed, which closes the HTTP connection
2. **Go cancels the request context** — the HTTP server detects the closed connection and cancels `ctx`
3. **Event loop exits** — `<-ctx.Done()` fires in the `Handler()` select loop
4. **Cleanup runs** — the deferred function unsubscribes every pub/sub subscription and the control channel
5. **New page connects** — the new page's `Connect()` opens a fresh SSE connection with its own scopes

There is no subscription leakage across page navigations. Each page gets an isolated set of subscriptions that are fully cleaned up when the connection closes.

```
 Page A                    Server                     Page B
 ──────                    ──────                     ──────
 Connect() opens SSE       Handler() subscribes
 with scopes A1, A2        to topics A1, A2
        │                        │
 User navigates ──────►   ctx.Done() fires
                           defer: unsubscribe A1, A2
                           handler returns
                                                      Connect() opens SSE
                                                      with scopes B1, B3
                                 │◄────────────────   Handler() subscribes
                                                      to topics B1, B3
```

#### Authentication and Token Expiry

The SSE connection is long-lived. Auth middleware (JWT validation, session cookies) runs **once** when the connection opens. If the token or session expires while the connection is active, the server has no built-in mechanism to detect this — the user continues receiving updates.

**Recommended approach: max connection duration with automatic reconnect.**

The stream handler enforces a maximum connection lifetime (e.g. 5 minutes). When the deadline is reached, the handler closes the connection. Datastar automatically reconnects, which re-runs the auth middleware on the new request. If the token has expired, the middleware rejects the reconnect and the stream stops.

```
 Browser                         Server (auth middleware → stream handler)
 ───────                         ──────────────────────────────────────────
 Connect SSE ──────────────────► Auth OK → subscribe, start event loop
        │                              │
   receives events              pushes stale signals
        │                              │
        │                        5 min deadline reached
        │◄──────────────────── connection closed
        │
 Datastar auto-reconnects ────► Auth middleware runs again:
                                  • Token valid → new connection, new 5 min window
                                  • Token expired → 401, stream stops
```

This approach is preferred because:

- **No auth coupling**: The stream handler doesn't know about tokens, sessions, or auth logic — it only manages a deadline
- **Works with any auth system**: JWT, session cookies, OAuth — whatever the middleware validates
- **Datastar handles reconnection natively**: No client-side code needed
- **Missed invalidations are harmless**: The stale-signal pattern means components simply refetch on the next push — there's no data loss, only a brief gap where a change might not trigger an immediate reload
- **Configurable**: The duration can be tuned per deployment — shorter for high-security apps, longer to reduce reconnection overhead

Alternatives considered:

| Approach | Downside |
|----------|----------|
| Heartbeat with token re-check | Couples stream handler to auth logic; requires importing auth packages |
| Session revocation via control channel | Requires auth system to publish to the stream's pub/sub; adds cross-system dependency |

### Security Considerations

- **Max scopes**: `maxScopes = 64` prevents memory exhaustion from malicious clients requesting excessive subscriptions
- **Signal namespace**: The `_stream` prefix (underscore) makes signals local-only in Datastar — they're never sent to the backend in requests
- **Scope validation**: Scopes are converted to pub/sub topics with simple character replacement; no injection vector exists since colons become dots and wildcards are native pub/sub syntax
- **Topic isolation**: The relay subscribes using `pubsub.ChangePattern` conventions that include tenant/workspace, ensuring scope isolation across tenants

### Performance Characteristics

| Aspect | Detail |
|--------|--------|
| SSE connections | One per browser tab (persistent, multiplexed across all scopes) |
| Pub/sub subscriptions | One per unique scope per connection (deduplicated) |
| Invalidation cost | One pub/sub publish per scope, fan-out handled by backend |
| Multi-watcher overhead | Extra JSON keys in SSE event; no extra pub/sub subscriptions |
| Grouped URL format | Reduces URL length for pages with many entity-scoped components |

## Integration Guide

### main.go Setup

A typical `main.go` creates the relay and bus, registers the stream route, and passes the bus to handlers:

```go
package main

import (
    "fmt"
    "net/http"
    "time"

    "github.com/go-chi/chi/v5"
    "github.com/nats-io/nats-server/v2/server"
    "github.com/nats-io/nats.go"
    "github.com/laenen-partners/pubsub"
    "github.com/laenen-partners/pubsub/natspubsub"
    "github.com/laenen-partners/dsx/stream"
)

func run() error {
    // 1. Create a pub/sub backend (NATS example with embedded server)
    ns, err := server.NewServer(&server.Options{DontListen: true})
    if err != nil {
        return fmt.Errorf("creating NATS server: %w", err)
    }
    ns.Start()
    defer ns.Shutdown()
    if !ns.ReadyForConnections(4 * time.Second) {
        return fmt.Errorf("NATS server not ready")
    }
    nc, err := nats.Connect(ns.ClientURL(), nats.InProcessServer(ns))
    if err != nil {
        return fmt.Errorf("connecting to NATS: %w", err)
    }
    defer nc.Close()

    ps := natspubsub.New(nc)

    // 2. Create the stream relay and pub/sub bus
    relay := stream.New(ps)
    bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope("default", "default"))

    // 3. Register routes
    r := chi.NewRouter()
    r.Get("/stream", relay.Handler())                      // SSE endpoint

    // 4. Pass bus to handlers that need to publish changes
    h := newCustomerHandlers(bus)
    h.registerRoutes(r)

    return http.ListenAndServe(":8080", r)
}
```

The relay is registered as a **single route** — not middleware:

| Route | Method | Purpose |
|-------|--------|---------|
| `/stream` | GET | Persistent SSE connection — `relay.Handler()` |

For alternative pub/sub backends, swap the backend constructor:

```go
// In-process (dev/single-process)
ps := chanpubsub.New()
relay := stream.New(ps)

// Redis (when Redis is already in the stack)
ps := redispubsub.New(redisClient)
relay := stream.New(ps)
```

### Layout Placement

`stream.Connect()` is placed in the base layout **after** `{ children... }` so all component scopes are accumulated before the SSE connection is rendered:

```go
templ Base(props BaseProps) {
    <!DOCTYPE html>
    <html lang="en">
        <head>...</head>
        <body>
            { children... }
            @stream.Connect()
        </body>
    </html>
}
```

### Handler Pattern

Handlers receive the bus and publish after mutations:

```go
type customerHandlers struct {
    bus *pubsub.Bus
}

func (h *customerHandlers) create() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // ... validate and save ...

        // Notify all watchers of customers:*
        h.bus.NotifyCreated(r.Context(), "customers", strconv.Itoa(id))

        sse := datastar.NewSSE(w, r)
        ds.Send.Toast(sse, ds.ToastSuccess, "Customer created")
    }
}
```

## Initial Load vs. Invalidation

There are two distinct mechanisms that keep the UI populated with data. Understanding when each applies is key to using streams correctly.

### Page Navigation (Initial Load)

When a user navigates to a page, components fetch their data **once** using `ds.GetOnce()` via `data-init`. No invalidation is involved:

1. Browser tears down the old page DOM — the previous SSE connection closes
2. Server detects the closed connection, cleans up all pub/sub subscriptions
3. New page renders — each component does a one-time fetch via `data-init` with `ds.GetOnce(reloadURL)`
4. `stream.Connect()` opens a fresh SSE connection subscribing to the new page's scopes

At this point the page is fully loaded with current data. The stream is now **listening** for future changes.

### Bus Publishing (Live Updates)

`bus.NotifyUpdated()` (and other `bus.Notify*` methods) is for when data changes **while users are already looking at the page**. It publishes to the change topic, and all browsers with an open SSE connection watching a matching scope receive a stale signal that triggers a refetch.

**Use cases:**

- **Another user made a change** — User A adds a customer, User B's list updates automatically
- **Cross-component sync** — Adding a customer updates both the table and the count stat
- **Cross-tab sync** — Same page open in two browser tabs, both stay in sync
- **Background processes** — A long-running job completes (import, report generation), the UI reflects it
- **External system events** — Kafka consumer, webhook, cron job triggers a UI update

**Publishing can be done from anywhere in your Go code**, not just HTTP handlers:

```go
// From an HTTP handler after a mutation
func (h *handler) create() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // ... save to database ...
        h.bus.NotifyCreated(r.Context(), "customers", strconv.Itoa(id))
    }
}

// From a background goroutine consuming external events
go func() {
    for event := range kafkaMessages {
        bus.NotifyUpdated(ctx, "orders", event.OrderID)
    }
}()

// From a webhook handler
func (h *handler) stripeWebhook() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // ... process webhook ...
        h.bus.NotifyUpdated(r.Context(), "payments", paymentID)
    }
}
```

The pub/sub doesn't care **who** publishes or **when** — it just delivers the message. As long as there's a browser with an open SSE connection watching a matching scope, it will receive the update.

### Summary

| Mechanism | When it runs | What triggers it | Purpose |
|-----------|-------------|------------------|---------|
| `ds.GetOnce()` | Page load | `data-init` on render | Initial data population |
| `bus.Notify*()` | Anytime after page load | Your code (handler, goroutine, webhook) | Live update of already-rendered components |

## Consequences

### Positive

- Components declare dependencies declaratively — no manual subscription wiring
- Cross-tab sync comes free from the pub/sub model
- Pub/sub backend is swappable without changing component code
- Wildcard scopes enable efficient fan-out (one subscription for all entities)
- Multi-watcher support allows independent components on the same data

### Negative

- Each browser tab maintains a persistent SSE connection
- The `keys` query param adds URL complexity for multi-watcher pages
- Wildcard matching in `chanpubsub` iterates all subscriptions (O(n) per publish) — acceptable for development, use NATS/Redis for high-throughput production
- Components must have a stable `id` on their root element for `ds.Send.Patch` to target

### Trade-offs

- **Stale-then-reload vs. push data**: The default pattern pushes a stale flag and the component refetches. Publishing with data can push data inline for small payloads, but for large or component-specific renders, the refetch pattern is cleaner.
- **Wildcard granularity**: `customers:*` reloads on any customer change. For large lists with frequent changes, consider more specific scopes or debouncing on the client side.
