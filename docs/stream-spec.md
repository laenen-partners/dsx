# Stream — DOM-Driven Watch Subscriptions with Pub/Sub

The `stream` package provides real-time reactivity for server-rendered applications. Components declare subscriptions via `data-watch` attributes in the DOM. A MutationObserver-based watch worker tracks these attributes and manages SSE connections automatically. The server pushes per-domain signals (e.g. `_ds_customers`) with `{id, action, ts}`, and components react via `data-effect` with action-aware conditions.

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
     |<-- _ds_counter "connected"--|-- _ds_counter "connected" -->|
     |                             |                              |
     |  POST /counter/increment -->|                              |
     |                             |-- bus.NotifyUpdated() -------->PubSub
     |                             |                              |
     |<-- _ds_counter signal ------|-- _ds_counter signal ------->|
     |  {id:"shared",              |  {id:"shared",               |
     |   action:"updated",         |   action:"updated",          |
     |   ts:1234567890}            |   ts:1234567890}             |
     |                             |                              |
     |  data-effect triggers       |  data-effect triggers        |
     |  GET /api/counter --------->|<------ GET /api/counter -----|
     |<---- fresh HTML ------------|-------- fresh HTML --------->|
```

### The Five Steps

1. **Declare** — Components spread `stream.Watch(ctx, domain, reactions...)` which adds `data-watch`, `data-signals`, and `data-effect` attributes to the element.
2. **Auto-connect** — The watch worker's MutationObserver detects `data-watch` attributes and opens a persistent SSE connection with all watched domains.
3. **Mutate** — A handler modifies data and calls `bus.NotifyUpdated(ctx, "entity", "id")` (using `pubsub.Bus`).
4. **Push** — The pub/sub backend delivers the message to the stream relay, which pushes a per-domain signal (e.g. `_ds_customers`) to all connected browsers via SSE.
5. **React** — The component's `data-effect` checks the action and optionally ID, then reloads itself with a fresh GET request.

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
// Pattern resolver — maps watch domains to pub/sub subscription patterns
resolver := func(_ context.Context, watch string) string {
    domain, entityID, hasID := strings.Cut(watch, ".")
    if !hasID || entityID == "" {
        return fmt.Sprintf("%s.%s.change.%s.>", tenant, workspace, domain)
    }
    return fmt.Sprintf("%s.%s.change.%s.%s.>", tenant, workspace, domain, entityID)
}
relay := stream.New(ps, resolver)
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

// Pattern resolver — maps watch domains to pub/sub subscription patterns
resolver := func(_ context.Context, watch string) string {
    domain, entityID, hasID := strings.Cut(watch, ".")
    if !hasID || entityID == "" {
        return fmt.Sprintf("%s.%s.change.%s.>", tenant, workspace, domain)
    }
    return fmt.Sprintf("%s.%s.change.%s.%s.>", tenant, workspace, domain, entityID)
}
relay := stream.New(ps, resolver)
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

// Pattern resolver — maps watch domains to pub/sub subscription patterns
resolver := func(_ context.Context, watch string) string {
    domain, entityID, hasID := strings.Cut(watch, ".")
    if !hasID || entityID == "" {
        return fmt.Sprintf("%s.%s.change.%s.>", tenant, workspace, domain)
    }
    return fmt.Sprintf("%s.%s.change.%s.%s.>", tenant, workspace, domain, entityID)
}
relay := stream.New(ps, resolver)
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
            stream.Structural.Get(wxctx.APIPath("/customers/list")))... }>
    </div>
}
```

### Row (in-place update, specific ID)

```go
templ CustomerRow(c Customer) {
    <div id={fmt.Sprintf("customer-row-%d", c.ID)}
        { stream.Watch(ctx, "customers",
            stream.Updated.ID(c.ID).Get(
                wxctx.APIPath(fmt.Sprintf("/customers/%d/row", c.ID))))... }>
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
            stream.Any.Get(wxctx.APIPath("/customers/count")))... }>
    </div>
}
```

### With debounce (bulk operations)

```go
templ CustomerList() {
    {{ wxctx := dsx.FromContext(ctx) }}
    <div id="customer-list"
        data-init={ds.GetOnce(wxctx.APIPath("/customers/list"))}
        { stream.Watch(ctx, "customers",
            stream.Structural.Debounce(300*time.Millisecond).Get(wxctx.APIPath("/customers/list")))... }>
    </div>
}
```

### Multiple reactions on one element

```go
<div id="customer-panel"
    { stream.Watch(ctx, "customers",
        stream.Structural.Get(wxctx.APIPath("/customers/list")),
        stream.Any.Get(wxctx.APIPath("/customers/count")))... }>
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
- `data-signals` — initializes the per-domain signal (e.g. `{_ds_customers: {id: '', action: '', ts: 0}}`)
- `data-effect` — action-aware expression(s) that trigger reloads

### `ActionSet` type

An `ActionSet` is the entry point for building reactions. Predefined action sets:

- **`stream.Created`** — matches `"created"` events
- **`stream.Updated`** — matches `"updated"` events
- **`stream.Deleted`** — matches `"deleted"` events
- **`stream.Any`** — matches any action (including `"connected"`)
- **`stream.Structural`** — matches `"created"` and `"deleted"` (equivalent to `stream.Created.Or(stream.Deleted)`)

### `Action("name") ActionSet`

Creates a custom `ActionSet` for a user-defined action name.

### `(ActionSet) Or(other ActionSet) ActionSet`

Combines two action sets into one that matches either. For example, `stream.Created.Or(stream.Updated)` matches both `"created"` and `"updated"` events.

### `(ActionSet) ID(id) *ReactionBuilder`

Filters a reaction to a specific entity ID. When used, the `data-watch` value becomes `domain.id` for more targeted subscriptions.

### `(ActionSet) Debounce(d time.Duration) *ReactionBuilder`

Adds a debounce delay to the reaction. When multiple events arrive in rapid succession (e.g. bulk creates), only the last one triggers the `@get()` after the delay elapses.

### `(ActionSet) Get(url) Reaction`

Finalizes the reaction with the URL to fetch when the reaction triggers. This is a shorthand for when no `.ID()` or `.Debounce()` is needed.

### `SignalKey(domain) string`

Returns the Datastar signal name for a domain (e.g. `_ds_customers`).

### `Relay.Handler() http.HandlerFunc`

SSE endpoint. Reads `?watch=domain1,domain2.id` query parameter. On initial connection, pushes a synthetic `"connected"` event for each watched domain so components can catch up after SSE reconnects.

## Architecture Notes

- **Per-domain signals** — Each domain gets its own Datastar signal (`_ds_customers`, `_ds_counter`). Events on one domain only trigger re-evaluation of effects referencing that domain's signal, avoiding the O(N) cost of a single global signal.
- **DOM-driven subscriptions** — `data-watch` attributes on elements ARE the subscription declarations. No render-time accumulation needed.
- **MutationObserver** — The watch worker scans for `data-watch` changes and manages SSE reconnects with debouncing (300ms). The hidden SSE div has `data-ignore-morph` to prevent conflicts with Datastar's Idiomorph.
- **Structured events** — The server pushes `{id, action, ts}` per domain so components can react to specific actions.
- **Action awareness** — A list can watch only `Structural` changes (created/deleted) while ignoring `Updated`. A count widget can watch `Any` (any action).
- **Reconnect protection** — On SSE connection, the relay pushes a `"connected"` event for each domain. Every effect matches `"connected"`, so components reload once after reconnect to catch any events missed during the gap.
- **Debounce** — Opt-in via `.Debounce(duration)` on the reaction builder. Wraps `@get()` in `setTimeout`/`clearTimeout` to collapse rapid events into a single fetch.
- **Last-event-wins** — Rapid consecutive events for the same domain overwrite the signal. This is acceptable because reactions always fetch fresh server state via `@get()` — the signal is a trigger, not the data.
- **`@get()` in data-effect** — Using action calls inside `data-effect` is an intentional pattern. It keeps the API surface minimal and avoids a second attribute for the same concern.
- **One SSE connection per tab** — the watch worker manages a single connection for all watched domains.
- **Datastar-native SSE** — the watch worker creates a hidden div with `data-init="@get('/stream?watch=...')"` so Datastar handles the SSE connection natively.
- **Backpressure** — the internal channel has a buffer of 64 messages. If a slow client can't keep up, excess messages are dropped.
- **Max watches** — each SSE connection is limited to 64 subscriptions.
- **Pluggable backends** — the `pubsub.PubSub` interface allows swapping backends without changing application code.
- **App-provided resolver** — The relay delegates subject-format decisions to a `PatternResolver` function supplied at construction time. The app controls how watch domains map to pub/sub subscription patterns (e.g. tenant/workspace scoping).
