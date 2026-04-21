# Getting Started with WebX

> **Module**: `github.com/laenen-partners/dsx`
> **Go Requirement**: Go 1.24+
> **Stack**: Go + Chi (routing) + Templ (templating) + Tailwind CSS + DaisyUI (styling) + Datastar (frontend interactivity)

---

## Installation

```bash
go get github.com/laenen-partners/dsx
```

Install dependencies with the task runner:

```bash
go tool task install:all    # mod tidy + download DaisyUI
go tool task install:daisyui # DaisyUI plugin files only
```

Generate templ code after editing any `.templ` file:

```bash
go tool templ generate
```

Build Tailwind CSS:

```bash
go tool gotailwind
```

---

## Project Structure

```
cmd/            -- application entry points
internal/       -- internal packages
ui/             -- DaisyUI components (one dir per component, e.g. ui/button/)
ds/             -- Datastar helpers (frontend attributes + backend SSE helpers)
layouts/        -- base HTML layout with toast/modal/drawer containers
stream/         -- reactive streaming via pluggable pub/sub
pubsub/         -- pub/sub interface + adapters (NATS, Redis, Go channels)
utils/          -- shared templ utilities (TwMerge, If, RandomID, etc.)
static/css/     -- Tailwind CSS + DaisyUI plugin files
docs/           -- reference documentation
```

---

## Minimal Server Setup

```go
package main

import (
    "fmt"
    "net/http"
    "time"

    "github.com/go-chi/chi/v5"
    "github.com/nats-io/nats-server/v2/server"
    "github.com/nats-io/nats.go"
    "github.com/laenen-partners/dsx"
    "github.com/laenen-partners/pubsub"
    "github.com/laenen-partners/pubsub/natspubsub"
    "github.com/laenen-partners/dsx/stream"
    "github.com/laenen-partners/dsx/ui"
    "github.com/laenen-partners/dsx/ui/calendar"
    "github.com/laenen-partners/dsx/ui/markdown"
    "github.com/laenen-partners/dsx/ui/moneyinput"
    "github.com/laenen-partners/dsx/ui/themecontroller"
)

func main() {
    // 1. Start embedded NATS (required for reactive streaming)
    ns, _ := server.NewServer(&server.Options{DontListen: true})
    ns.Start()
    defer ns.Shutdown()
    ns.ReadyForConnections(4 * time.Second)

    nc, _ := nats.Connect(ns.ClientURL(), nats.InProcessServer(ns))
    defer nc.Close()

    ps := natspubsub.New(nc)

    // Pattern resolver — maps watch domains to pub/sub subscription patterns
    resolver := func(_ context.Context, watch string) string {
        domain, entityID, hasID := strings.Cut(watch, ".")
        if !hasID || entityID == "" {
            return fmt.Sprintf("default.default.change.%s.>", domain)
        }
        return fmt.Sprintf("default.default.change.%s.%s.>", domain, entityID)
    }
    relay := stream.New(ps, resolver)
    bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope("default", "default"))

    // 2. Create router with middleware
    r := chi.NewRouter()

    secret := []byte("your-secret-key-at-least-32-bytes!") // use a real secret in production
    r.Use(dsx.Middleware(dsx.MiddlewareConfig{
        Secret: secret,
        Secure: false, // set true in production (HTTPS)
    }))
    r.Use(dsx.SecurityHeadersMiddleware()) // pass true for HSTS: SecurityHeadersMiddleware(true)

    // 3. Configure WebX context
    const basePath = "/app"
    r.Use(func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            wctx := dsx.FromContext(r.Context())
            wctx.BasePath = basePath
            wctx.StreamURL = basePath + "/stream"
            next.ServeHTTP(w, r.WithContext(wctx.WithContext(r.Context())))
        })
    })

    // 4. Register pages
    r.Get("/", templ.Handler(pages.Home()).ServeHTTP)

    // 5. Register SSE handlers
    r.Route(basePath, func(r chi.Router) {
        r.Get("/stream", relay.Handler())
        ui.RegisterRoutes(r,
            calendar.Route(),
            themecontroller.Route(false),
            markdown.Route(),
            moneyinput.DecimalRoute(),
            moneyinput.MoneyRoute(),
        )
    })

    http.ListenAndServe(":3000", r)
}
```

---

## WebX Context

Every request carries a `dsx.Context` set up by `dsx.Middleware`. Access it in handlers and templ components:

```go
wctx := dsx.FromContext(ctx)
```

| Field | Type | Description |
|---|---|---|
| `CSRFToken` | `string` | Auto-generated signed CSRF token (cookie-based double-submit) |
| `SessionID` | `string` | Session cookie value (random hex, auto-generated) |
| `BasePath` | `string` | Prefix for SSE handler routes (e.g. `"/app"`) |
| `StreamURL` | `string` | URL for the reactive SSE stream endpoint |
| `Theme` | `string` | DaisyUI theme name (persisted in cookie) |
| `Scopes` | `[]string` | Reactive scopes accumulated during render |

Helper: `wctx.APIPath("/my-endpoint")` prepends `BasePath` to a path.

---

## Middleware

WebX uses cookie-based middleware. No session store is required.

| Middleware | What it does |
|---|---|
| `dsx.Middleware(cfg)` | Creates/reads session, CSRF, and theme cookies; populates `dsx.Context`; validates signed CSRF token on mutating requests (POST/PUT/DELETE) |
| `dsx.SecurityHeadersMiddleware()` | Sets `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Content-Security-Policy` headers (includes `unsafe-eval` for Datastar) |
| `dsx.SecurityHeadersMiddleware(true)` | Same as above, plus `Strict-Transport-Security` (HSTS) for HTTPS environments |

### MiddlewareConfig

```go
dsx.MiddlewareConfig{
    Secret: []byte("..."), // HMAC-SHA256 key, minimum 32 bytes (panics if shorter)
    Secure: true,          // Set Secure flag on cookies (true for HTTPS / production)
}
```

CSRF protection uses a signed double-submit cookie pattern. The middleware generates a cryptographically signed token, stores it in an `HttpOnly` cookie, and validates it via the `X-CSRF-Token` header on mutating requests. The `ds` package automatically includes this header in Datastar POST/PUT/DELETE actions.

---

## Handler Registration

`ui.RegisterRoutes` applies route options to a router. Each component package exports its own `Route()` function:

```go
r.Route(basePath, func(r chi.Router) {
    r.Get("/stream", relay.Handler())

    ui.RegisterRoutes(r,
        calendar.Route(),                              // GET  /calendar/navigate
        themecontroller.Route(false),                   // POST /theme
        markdown.Route(),                              // POST /preview/markdown
        moneyinput.DecimalRoute(),                     // GET  /parse/decimal
        moneyinput.MoneyRoute("USD", "EUR"),           // GET  /parse/money
        fileupload.Route(store),                       // POST /upload/files + /upload/remove
        fileupload.Route(store,                        // with validation options
            fileupload.WithMaxFiles(3),
            fileupload.WithAllowedTypes("image/"),
        ),
    )
})
```

**Handlers that require app-specific logic** (register manually):

```go
// Validator — each field needs its own validation function
r.Get("/validate/email", validator.Handler(emailValidator))

// Form — signals struct + validation function + success callback
r.Post("/auth/login", form.Handler(loginSignals{}, loginValidator, onSuccess))
```

Each handler package exports a path constant (e.g. `markdown.PreviewPath`, `moneyinput.DecimalPath`, `fileupload.UploadPath`) for use when registering handlers manually.

---

## Components

Components live in `ui/` and follow a consistent pattern:

```go
import "github.com/laenen-partners/dsx/ui/button"

// Use with optional props
@button.Button(button.Props{Variant: button.VariantPrimary}) {
    Click me
}

// Or with zero-value defaults
@button.Button() {
    Click me
}
```

All components accept `Class` (extra Tailwind classes) and `Attributes` (extra HTML attributes) props. Classes are merged with `utils.TwMerge()`.

See [Component Migration Guide](./component-migration-guide.md) for the full component authoring pattern.

---

## Frontend Interactivity (`ds` Package)

The `ds` package provides type-safe Go helpers for Datastar attributes. Use these in templ files instead of raw `data-*` strings.

**Attribute helpers** (return `templ.Attributes`):

```go
ds.On("click", expr)       // data-on:click="expr"
ds.OnClick(expr)            // shorthand
ds.Bind("fieldName")        // data-bind:fieldName
ds.Signals(`{"count": 0}`)  // data-signals
ds.Show("$count > 0")       // data-show
ds.Text("$count")           // data-text
```

**Action expressions** (return `string` for use in `ds.On`/`ds.OnClick`):

```go
ds.Get("/data")             // @get('/data') with retry
ds.Post("/submit")          // @post('/submit') with CSRF token
ds.Put("/update")           // @put with CSRF
ds.Delete("/remove")        // @delete with CSRF
```

See [Datastar Go Reference](./datastar-go-reference.md) for the full backend SDK API and [HTML Datastar Elements Reference](./html-datastar-elements-reference.md) for all frontend attributes.

---

## Backend SSE Helpers (`ds.Send`)

Send UI actions from SSE handlers:

```go
func myHandler(w http.ResponseWriter, r *http.Request) {
    sse := datastar.NewSSE(w, r)

    // Toast notification
    ds.Send.Toast(sse, ds.ToastSuccess, "Saved!")

    // Show modal with templ content
    ds.Send.Modal(r.Context(), sse, myModalContent())

    // Show drawer
    ds.Send.Drawer(r.Context(), sse, myDrawerContent())

    // Confirmation dialog
    ds.Send.Confirm(sse, "Delete this item?", "/items/delete")

    // Patch a DOM element with a templ component
    ds.Send.Patch(sse, myComponent())

    // Navigation
    ds.Send.Redirect(sse, "/dashboard")
    ds.Send.Download(sse, "/files/report.pdf", "report.pdf")
}
```

The base layout includes container elements (`#modal-panel`, `#drawer-panel`, `#toast-container`) that these helpers target.

---

## Reactive Streaming

The `stream` package enables real-time updates via pluggable pub/sub (NATS, Redis, or Go channels):

```go
// In a templ component — watch a scope and re-fetch on invalidation
@stream.WatchEffect(ctx, "invoice:42", "/invoice/42")

// In a handler — publish after mutation (using pubsub.Bus)
bus.NotifyUpdated(ctx, "invoice", "42")
```

See [Datastar Go Reference](./datastar-go-reference.md) for streaming patterns and scope conventions.

---

## Further Reading

- [Datastar Go Reference](./datastar-go-reference.md) — full backend SDK API, SSE events, patterns
- [HTML Datastar Elements Reference](./html-datastar-elements-reference.md) — all HTML elements and Datastar attribute wiring
- [Component Migration Guide](./component-migration-guide.md) — converting DaisyUI patterns into WebX templ components
