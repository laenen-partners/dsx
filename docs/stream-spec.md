# Stream — Reactive SSE with Pub/Sub

The `stream` package provides real-time reactivity for server-rendered applications. Components declare what data they depend on, and the system automatically keeps every connected browser tab in sync when that data changes.

## How It Works

```
Browser Tab A                    Server                      Browser Tab B
     │                             │                              │
     │  SSE connect ──────────────>│                              │
     │  scope=invoice:42           │                              │
     │                             │<────────── SSE connect ──────│
     │                             │   scope=invoice:42           │
     │                             │                              │
     │  POST /invoice/42/save ────>│                              │
     │                             │── broker.Invalidate() ──>PubSub
     │                             │                              │
     │<──── stale signal ──────────│──── stale signal ──────────> │
     │  _stream.invoice_42=true    │  _stream.invoice_42=true     │
     │                             │                              │
     │  GET /api/invoice/42 ──────>│<────── GET /api/invoice/42 ──│
     │<──── fresh HTML ────────────│──────── fresh HTML ─────────>│
```

### The Five Steps

1. **Register** — Components call `stream.WatchEffect(ctx, scope, reloadURL)` during render to declare data dependencies.
2. **Connect** — The layout renders `stream.Connect()` which opens a persistent SSE connection scoped to the registered entities.
3. **Mutate** — A handler modifies data and calls `broker.Invalidate("scope")` (or `InvalidateWithData`).
4. **Push** — The pub/sub backend delivers the message to the stream handler, which pushes a stale signal to all connected browsers via SSE.
5. **Reload** — The component's `data-effect` detects the stale flag and auto-reloads itself with a fresh GET request.

## Pub/Sub Adapters

The broker accepts any `pubsub.PubSub` implementation. Three adapters are provided:

| Adapter | Package | Use Case |
|---------|---------|----------|
| **NATS** | `pubsub/natspubsub` | Production — wraps `*nats.Conn` |
| **Redis** | `pubsub/redispubsub` | Production — wraps `*redis.Client` (PUBLISH/PSUBSCRIBE) |
| **Go channels** | `pubsub/chanpubsub` | Development & testing — zero external deps |

All adapters support dot-separated topics with wildcards: `*` matches one segment, `>` matches the rest.

## Setup

### With NATS (production)

```go
import (
    "github.com/nats-io/nats-server/v2/server"
    "github.com/nats-io/nats.go"
    "github.com/laenen-partners/dsx/pubsub/natspubsub"
    "github.com/laenen-partners/dsx/stream"
)

// 1. Create a NATS connection (embedded or external)
ns, _ := server.NewServer(&server.Options{DontListen: true})
ns.Start()
nc, _ := nats.Connect(ns.ClientURL(), nats.InProcessServer(ns))

// 2. Create a broker with the NATS adapter
broker := stream.NewBroker(natspubsub.New(nc))

// 3. Wire the SSE endpoint
r.Get("/stream", broker.Handler())

// 4. (Optional) Wire dynamic scope subscription endpoint
r.Post("/stream/subscribe", broker.SubscribeHandler())
```

### With Redis

```go
import (
    "github.com/redis/go-redis/v9"
    "github.com/laenen-partners/dsx/pubsub/redispubsub"
    "github.com/laenen-partners/dsx/stream"
)

client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
broker := stream.NewBroker(redispubsub.New(client))
```

### With Go channels (dev/testing)

```go
import (
    "github.com/laenen-partners/dsx/pubsub/chanpubsub"
    "github.com/laenen-partners/dsx/stream"
)

broker := stream.NewBroker(chanpubsub.New())
```

## Usage in Templates

```go
// In your component template:
templ InvoiceCard(invoice Invoice) {
    {{
        wxctx := dsx.FromContext(ctx)
        scope := fmt.Sprintf("invoice:%d", invoice.ID)
        reloadURL := wxctx.APIPath(fmt.Sprintf("/invoice/%d", invoice.ID))
        effect := stream.WatchEffect(ctx, scope, reloadURL)
    }}
    <div
        data-signals={ stream.ScopeSignals(scope) }
        { ds.Effect(effect)... }
    >
        <span id={ fmt.Sprintf("invoice-%d", invoice.ID) }
            { ds.Init(ds.GetOnce(reloadURL))... }>
            Loading...
        </span>
    </div>
}

// In your layout (AFTER {children...}):
@stream.Connect()
```

## Usage in Handlers

```go
// After mutating data:
func (h *handler) updateInvoice(w http.ResponseWriter, r *http.Request) {
    invoice := updateInDB(r)

    // Option A: Simple invalidation — clients refetch the component
    broker.Invalidate(fmt.Sprintf("invoice:%d", invoice.ID))

    // Option B: Invalidation with data — clients get the entity in the SSE event
    broker.InvalidateWithData(fmt.Sprintf("invoice:%d", invoice.ID), invoice)

    // Option C: Invalidate multiple scopes at once
    broker.InvalidateMany("invoice:42", "invoices:list", "dashboard:stats")

    datastar.NewSSE(w, r) // close the mutation SSE cleanly
}
```

## Features

### Compact Scope Query Format

The SSE connection URL uses comma-separated scopes for compact URLs:

```
/stream?scope=invoice:42,invoices:list,dashboard:stats
```

Both comma-separated and repeated params are supported (backward compatible):

```
/stream?scope=invoice:42&scope=invoices:list   // also works
```

### Scope Payload Data (InvalidateWithData)

Carry entity data alongside the stale signal. The data is JSON-encoded and delivered under the `_streamData` signal namespace:

```go
broker.InvalidateWithData("invoice:42", invoice)
```

The SSE event contains both namespaces:

```json
{
    "_stream":     {"invoice_42": true},
    "_streamData": {"invoice_42": {"id": 42, "total": 1500}}
}
```

This lets components optionally use the pushed data for optimistic UI updates instead of making a separate GET request.

### Dynamic Scope Registration

Add subscriptions to a live SSE connection without reconnecting. Useful when new components are loaded after the initial page render (e.g., modals, drawers, infinite scroll items).

**Server-side (from a handler):**

```go
broker.AddScope(sessionID, "invoice:99")
```

**Client-side (via POST endpoint):**

```
POST /stream/subscribe
Content-Type: application/x-www-form-urlencoded

scope=invoice:99
```

The session's SSE handler receives a control message and subscribes to the new scope in real-time.

### Wildcard Scopes

Scopes support wildcard patterns (supported by all adapters):

```go
// Subscribe to ALL invoice changes
stream.WatchEffect(ctx, "invoices:*", "/api/invoices")

// Invalidate a specific invoice — all wildcard subscribers receive it
broker.Invalidate("invoices:42")
```

### InitScope (Late Registration)

For components that appear after initial render (infinite scroll, lazy-loaded panels):

```go
func (h *handler) loadMore(w http.ResponseWriter, r *http.Request) {
    sse := datastar.NewSSE(w, r)
    for _, item := range items {
        scope := fmt.Sprintf("item:%d", item.ID)
        stream.InitScope(sse, scope) // push signal initialization
        _ = sse.PatchElements(renderItem(item))
    }
}
```

## Use Cases

### Real-Time Dashboards

Multiple widgets showing different data (revenue, orders, user count). Each widget watches its own scope. When any metric changes, only the affected widget reloads:

```go
stream.WatchEffect(ctx, "metrics:revenue", "/api/metrics/revenue")
stream.WatchEffect(ctx, "metrics:orders", "/api/metrics/orders")
```

### Collaborative Editing

Multiple users editing the same entity. When user A saves changes, user B's view updates automatically:

```go
// User A saves
broker.Invalidate("document:123")

// User B's browser receives stale signal and reloads the document
```

### Live Notifications

A notification bell that updates across all tabs when new notifications arrive:

```go
stream.WatchEffect(ctx, "notifications:user:42", "/api/notifications/count")

// When a new notification is created:
broker.Invalidate("notifications:user:42")
```

### Shopping Cart Sync

Cart count in the navbar stays in sync across all tabs:

```go
stream.WatchEffect(ctx, "cart:session:abc", "/api/cart/count")

// After adding an item in any tab:
broker.Invalidate("cart:session:abc")
```

### Admin Panels with Live Data

An admin panel showing a list of orders. When any order status changes (from a webhook, background job, or another admin), the list updates:

```go
// List page watches the wildcard
stream.WatchEffect(ctx, "orders:*", "/api/orders")

// Detail page watches specific order
stream.WatchEffect(ctx, "order:42", "/api/orders/42")

// Background job updates order status
broker.Invalidate("orders:42")  // triggers both watchers
```

### Optimistic Updates with Payload Data

Push entity data directly so the client can show it immediately without a round-trip:

```go
// After creating a new comment
broker.InvalidateWithData("comments:post:1", map[string]any{
    "count": newCount,
    "latest": comment.Preview,
})
```

The client receives both the stale flag (triggering a full reload) and the payload (available for immediate display in a `data-effect` expression).

## Architecture Notes

- **One SSE connection per tab** — each browser tab opens its own connection. The pub/sub backend handles fan-out efficiently.
- **No custom JavaScript** — all reactivity is driven by Datastar's `data-effect` and `data-signals` attributes.
- **Scopes are colon-separated** — `entity:id` pattern maps to pub/sub topics (`dsx.scope.entity.id`).
- **Stale-then-reload pattern** — the stream doesn't push HTML. It pushes a "stale" flag, and the component reloads itself. This keeps the SSE payload tiny and lets components own their rendering.
- **Session-scoped control channel** — dynamic scope registration uses a per-session topic (`{prefix}.ctrl.{sessionID}`) for isolation.
- **Backpressure** — the internal channel has a buffer of 64 messages. If a slow client can't keep up, excess messages are dropped (the next invalidation will catch up).
- **Max scopes** — each SSE connection is limited to 64 subscriptions to prevent resource exhaustion.
- **Pluggable backends** — the `pubsub.PubSub` interface allows swapping backends (NATS, Redis, Go channels) without changing application code. Use `chanpubsub` for development/testing and NATS or Redis for production.
