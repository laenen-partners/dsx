# Stream — DOM-Driven Watch Subscriptions with Pub/Sub

The `stream` package provides real-time reactivity for server-rendered applications. Components declare subscriptions via `data-watch` attributes in the DOM. A MutationObserver-based watch worker tracks these attributes and manages SSE connections automatically. The server pushes structured `_dsEvent` signals with `{domain, id, action, ts}`, and components react via `data-effect` with action-aware conditions.

## How It Works

```
Browser Tab A                    Server                      Browser Tab B
     |                             |                              |
     |  [data-watch="counter.shared" detected by MutationObserver]|
     |                             |                              |
     |  SSE connect -------------->|                              |
     |  ?watch=counter.shared      |                              |
     |                             |<---------- SSE connect ------|
     |                             |   ?watch=counter.shared      |
     |                             |                              |
     |  POST /counter/increment -->|                              |
     |                             |-- bus.NotifyUpdated() -------->PubSub
     |                             |                              |
     |<---- _dsEvent signal -------|---- _dsEvent signal -------->|
     |  {domain:"counter",         |  {domain:"counter",          |
     |   id:"shared",              |   id:"shared",               |
     |   action:"updated",         |   action:"updated",          |
     |   ts:1234567890}            |   ts:1234567890}             |
     |                             |                              |
     |  data-effect triggers       |  data-effect triggers        |
     |  GET /api/counter --------->|<------ GET /api/counter -----|
     |<---- fresh HTML ------------|-------- fresh HTML --------->|
```

### The Five Steps

1. **Declare** — Components spread `stream.Watch(ctx, domain, reactions...)` which adds `data-watch` and `data-effect` attributes to the element.
2. **Auto-connect** — The watch worker's MutationObserver detects `data-watch` attributes and opens a persistent SSE connection with all watched domains.
3. **Mutate** — A handler modifies data and calls `bus.NotifyUpdated(ctx, "entity", "id")` (using `pubsub.Bus`).
4. **Push** — The pub/sub backend delivers the message to the stream relay, which pushes a structured `_dsEvent` signal to all connected browsers via SSE.
5. **React** — The component's `data-effect` checks the event domain, action, and optionally ID, then reloads itself with a fresh GET request.

## Pub/Sub Adapters

The relay accepts any `pubsub.PubSub` implementation. Three adapters are provided:

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
    "github.com/laenen-partners/pubsub/natspubsub"
    "github.com/laenen-partners/dsx/stream"
    "github.com/laenen-partners/pubsub"
)

// 1. Create a NATS connection (embedded or external)
ns, _ := server.NewServer(&server.Options{DontListen: true})
ns.Start()
nc, _ := nats.Connect(ns.ClientURL(), nats.InProcessServer(ns))
ps := natspubsub.New(nc)

// 2. Create a relay and a bus
relay := stream.New(ps)
bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope(tenant, workspace))

// 3. Wire the SSE endpoint
r.Get("/stream", relay.Handler())
```

### With Redis

```go
import (
    "github.com/redis/go-redis/v9"
    "github.com/laenen-partners/pubsub/redispubsub"
    "github.com/laenen-partners/dsx/stream"
    "github.com/laenen-partners/pubsub"
)

client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
ps := redispubsub.New(client)
relay := stream.New(ps)
bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope(tenant, workspace))
```

### With Go channels (dev/testing)

```go
import (
    "github.com/laenen-partners/pubsub/chanpubsub"
    "github.com/laenen-partners/dsx/stream"
    "github.com/laenen-partners/pubsub"
)

ps := chanpubsub.New()
relay := stream.New(ps)
bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope(tenant, workspace))
```

## Usage in Templates

### List (structural changes only)

```go
templ CustomerList() {
    {{ wxctx := dsx.FromContext(ctx) }}
    <div id="customer-list"
        data-init={ds.GetOnce(wxctx.APIPath("/customers/list"))}
        { stream.Watch(ctx, "customers",
            stream.Reload("created,deleted", wxctx.APIPath("/customers/list")))... }>
    </div>
}
```

### Row (in-place update, specific ID)

```go
templ CustomerRow(c Customer) {
    <div id={fmt.Sprintf("customer-row-%d", c.ID)}
        { stream.Watch(ctx, "customers",
            stream.Reload("updated",
                wxctx.APIPath(fmt.Sprintf("/customers/%d/row", c.ID)),
                stream.WithID(c.ID)))... }>
    </div>
}
```

### Dashboard stat (any action)

```go
templ CustomerCount() {
    {{ wxctx := dsx.FromContext(ctx) }}
    <div id="customer-count"
        data-init={ds.GetOnce(wxctx.APIPath("/customers/count"))}
        { stream.Watch(ctx, "customers",
            stream.Reload("*", wxctx.APIPath("/customers/count")))... }>
    </div>
}
```

### Multiple reactions on one element

```go
<div id="customer-panel"
    { stream.Watch(ctx, "customers",
        stream.Reload("created,deleted", wxctx.APIPath("/customers/list")),
        stream.Reload("*", wxctx.APIPath("/customers/count")))... }>
</div>
```

## Usage in Handlers

```go
// After mutating data:
func (h *handler) updateInvoice(w http.ResponseWriter, r *http.Request) {
    invoice := updateInDB(r)

    // All browsers watching "invoice" will receive an event
    h.bus.NotifyUpdated(r.Context(), "invoice", strconv.Itoa(invoice.ID))

    datastar.NewSSE(w, r) // close the mutation SSE cleanly
}
```

## API Reference

### `Watch(ctx, domain, reactions...) templ.Attributes`

Returns `templ.Attributes` with:
- `data-watch` — declares the subscription (e.g. `"customers"` or `"customers.42"`)
- `data-effect` — action-aware expression(s) that trigger reloads

### `Reload(actions, url, opts...) Reaction`

Creates a reaction. `actions` is comma-separated (`"created,deleted"`) or `"*"` for any action.

### `WithID(id) ReloadOption`

Filters a reaction to a specific entity ID. When used, the `data-watch` value becomes `domain.id` for more targeted subscriptions.

### `EventSignals() string`

Returns the initial `data-signals` value for `_dsEvent`. Used internally by the watch worker.

### `Relay.Handler() http.HandlerFunc`

SSE endpoint. Reads `?watch=domain1,domain2.id` query parameter.

## Architecture Notes

- **DOM-driven subscriptions** — `data-watch` attributes on elements ARE the subscription declarations. No render-time accumulation needed.
- **MutationObserver** — The watch worker scans for `data-watch` changes and manages SSE reconnects with debouncing (300ms).
- **Structured events** — Instead of boolean stale flags, the server pushes `{domain, id, action, ts}` so components can react to specific actions.
- **Action awareness** — A list can watch only `"created,deleted"` (structural changes) while ignoring `"updated"`. A count widget can watch `"*"` (any action).
- **One SSE connection per tab** — the watch worker manages a single connection for all watched domains.
- **Datastar-native SSE** — the watch worker creates a hidden div with `data-init="@get('/stream?watch=...')"` so Datastar handles the SSE connection natively.
- **Backpressure** — the internal channel has a buffer of 64 messages. If a slow client can't keep up, excess messages are dropped.
- **Max watches** — each SSE connection is limited to 64 subscriptions.
- **Pluggable backends** — the `pubsub.PubSub` interface allows swapping backends without changing application code.
