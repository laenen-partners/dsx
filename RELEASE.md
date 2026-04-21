# Release: External Pub/Sub & Stream Relay

## Summary

This release removes the local pub/sub implementation from dsx, replaces it with the external `github.com/laenen-partners/pubsub` package, and redesigns the stream package around a clean subscribe-only `Relay` that integrates with the pub/sub `Bus` conventions.

**Key wins:**
- NATS, Redis, and testcontainers removed from dsx's dependency tree
- Stream package is now subscribe-only — publishing uses the standard `pubsub.Bus` API
- Showcase uses in-process channels (zero external deps for development)
- Topic format aligns with `pubsub.ChangePattern` conventions (tenant/workspace scoping)

---

## Changelog

### Breaking changes

#### `stream` package

| Before | After |
|--------|-------|
| `stream.Broker` | `stream.Relay` |
| `stream.NewBroker(ps, opts...)` | `stream.New(ps, opts...)` |
| `broker.Handler()` | `relay.Handler()` |
| `broker.Invalidate("scope")` | Removed — use `bus.NotifyUpdated(ctx, entity, id)` |
| `broker.InvalidateWithData("scope", data)` | Removed — use `bus.PublishChange(ctx, ...)` |
| `broker.InvalidateMany("a", "b")` | Removed — call `bus.Notify*` per entity |
| `broker.AddScope(sessionID, scope)` | Removed |
| `broker.SubscribeHandler()` | Removed |
| `broker.PubSub()` | Removed — app holds its own `pubsub.Bus` reference |
| `broker.Topic(ctx, scope)` | Removed — Relay builds topics internally |
| `stream.WithSubjectPrefix(prefix)` | Removed — topics follow `pubsub.ChangePattern` |

#### `showcase` package

| Before | After |
|--------|-------|
| `Setup func(ctx, r, broker *stream.Broker) error` | `Setup func(ctx, r, bus *pubsub.Bus, relay *stream.Relay) error` |

#### Removed packages

- `dsx/pubsub` — replaced by `github.com/laenen-partners/pubsub`
- `dsx/pubsub/chanpubsub` — replaced by `github.com/laenen-partners/pubsub/chanpubsub`
- `dsx/pubsub/natspubsub` — replaced by `github.com/laenen-partners/pubsub/natspubsub`
- `dsx/pubsub/redispubsub` — replaced by `github.com/laenen-partners/pubsub/redispubsub`
- `dsx/pubsub/pubsubtest` — replaced by `github.com/laenen-partners/pubsub/pubsubtest`

#### Removed dependencies from go.mod

- `github.com/nats-io/nats-server/v2`
- `github.com/nats-io/nats.go`
- `github.com/redis/go-redis/v9`
- `github.com/testcontainers/testcontainers-go/modules/nats`
- `github.com/testcontainers/testcontainers-go/modules/redis`
- `github.com/spf13/cobra` (no longer direct)

### New features

- **`stream.Relay`** — subscribe-only type that relays pub/sub change notifications to SSE clients
- **`pubsub.Bus` integration** — scopes map directly to `pubsub.ChangePattern` topics
- **`showcase.Config.Setup`** receives both `*pubsub.Bus` and `*stream.Relay`
- **`PORT` env var** — showcase server respects `PORT` environment variable
- **Identity switcher** added to `cmd/showcase` Dashboard layout navbar

### Internal improvements

- `cmd/showcase` rewritten to use `showcase.Run()` (254 → ~130 lines)
- `cmd/showcase/internal/static` package removed (was re-exporting `dsx.Static`)
- Showcase logs actual bound port (fixes `PORT=0` for random port in tests)
- E2E tests updated — no longer require embedded NATS server

---

## Migration guide

### 1. Update imports

```diff
- "github.com/laenen-partners/dsx/pubsub"
- "github.com/laenen-partners/dsx/pubsub/chanpubsub"
- "github.com/laenen-partners/dsx/pubsub/natspubsub"
- "github.com/laenen-partners/dsx/pubsub/redispubsub"
+ "github.com/laenen-partners/pubsub"
+ "github.com/laenen-partners/pubsub/chanpubsub"
+ "github.com/laenen-partners/pubsub/natspubsub"
+ "github.com/laenen-partners/pubsub/redispubsub"
```

### 2. Add external dependency

```bash
go get github.com/laenen-partners/pubsub@latest
```

### 3. Update PubSub method signatures

The external `pubsub.PubSub` interface adds `context.Context` to all methods:

```diff
- ps.Publish(topic, data)
+ ps.Publish(ctx, topic, data)

- ps.Subscribe(topic, handler)
+ ps.Subscribe(ctx, topic, handler)

- ps.Close()
+ ps.Close(ctx)
```

### 4. Replace Broker with Relay + Bus

**Before:**

```go
broker := stream.NewBroker(ps)

r.Get("/stream", broker.Handler())
r.Post("/stream/subscribe", broker.SubscribeHandler())
```

**After:**

```go
bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope(tenantID, workspaceID))

// Pattern resolver — maps watch domains to pub/sub subscription patterns
resolver := func(_ context.Context, watch string) string {
    domain, entityID, hasID := strings.Cut(watch, ".")
    if !hasID || entityID == "" {
        return fmt.Sprintf("%s.%s.change.%s.>", tenantID, workspaceID, domain)
    }
    return fmt.Sprintf("%s.%s.change.%s.%s.>", tenantID, workspaceID, domain, entityID)
}
relay := stream.New(ps, resolver)

r.Get("/stream", relay.Handler())
// SubscribeHandler route removed — no replacement needed
```

### 5. Replace Invalidate with Bus notifications

**Before:**

```go
broker.Invalidate("customer:42")
broker.InvalidateWithData("invoice:99", invoice)
broker.InvalidateMany("orders:1", "orders:2")
```

**After:**

```go
bus.NotifyUpdated(ctx, "customer", "42")
// For data payloads, use PublishChange or domain events
bus.NotifyCreated(ctx, "orders", "1")
bus.NotifyCreated(ctx, "orders", "2")
```

The scope `"customer:42"` in `WatchEffect`/`Attrs` now subscribes to `{tenant}.{workspace}.change.customer.42.>`, which matches what `bus.NotifyUpdated(ctx, "customer", "42")` publishes.

### 6. Update showcase Setup callback

**Before:**

```go
Setup: func(ctx context.Context, r chi.Router, broker *stream.Broker) error {
    h := handlers.New(broker)
    // ...
}
```

**After:**

```go
Setup: func(ctx context.Context, r chi.Router, bus *pubsub.Bus, relay *stream.Relay) error {
    h := handlers.New(bus, relay)
    // ...
}
```

### 7. Remove WithSubjectPrefix

If you used `stream.WithSubjectPrefix()`, remove it. Topic format is now determined by `pubsub.ChangePattern` conventions and scoped by the identity on the request context.

### 8. Run go mod tidy

```bash
go mod tidy
```

This removes the heavy NATS/Redis/testcontainer dependencies if you no longer import them directly.

---

## Scope-to-topic mapping

The Relay maps stream scopes to pub/sub change patterns using the identity from the request context:

| Stream scope | Pub/sub subscription pattern |
|-------------|------------------------------|
| `customer:42` | `{tenant}.{workspace}.change.customer.42.>` |
| `customers:*` | `{tenant}.{workspace}.change.customers.*.*` |
| `customer:>` | `{tenant}.{workspace}.change.customer.>` |

Publishing via the Bus:

| Bus method | Pub/sub topic |
|-----------|---------------|
| `bus.NotifyCreated(ctx, "customer", "42")` | `{tenant}.{workspace}.change.customer.42.created` |
| `bus.NotifyUpdated(ctx, "invoice", "99")` | `{tenant}.{workspace}.change.invoice.99.updated` |
| `bus.NotifyDeleted(ctx, "order", "1")` | `{tenant}.{workspace}.change.order.1.deleted` |

The scope `"customer:42"` subscribes to `change.customer.42.>`, which matches `created`, `updated`, `deleted`, and any other action.
