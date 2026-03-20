# dsx

A server-rendered component framework for Go. Built on [Templ](https://templ.guide), [DaisyUI](https://daisyui.com), and [Datastar](https://data-star.dev).

```
Browser                          Server (Go)
  |                                |
  |  GET /page ------------------>|  templ renders HTML
  |<---- full HTML page ----------|
  |                                |
  |  SSE @get('/fragment') ------>|  handler returns SSE events
  |<---- patch DOM element -------|  (PatchElements, PatchSignals)
  |                                |
  |  SSE stream /stream --------->|  stream.Relay listens to pub/sub
  |<---- stale signal ------------|  component auto-reloads
```

## Why dsx

- **Server-side rendering** with Go and Templ. No Node.js build step.
- **~15KB of JavaScript** total. Datastar handles all frontend interactivity.
- **70+ pre-built components** styled with DaisyUI. Accordions to YAML trees.
- **Real-time updates** across all browser tabs via pub/sub-backed SSE streaming.
- **Type-safe Datastar** helpers. No string typos in `data-on:click` attributes.
- **You own the code.** Fork it, edit it, ship it. No versioning conflicts.

## Quick start

```bash
# Prerequisites: Go 1.24+

# Install dependencies
go tool task install:all

# Generate templ + build Tailwind CSS
go tool templ generate
go tool gotailwind

# Run the showcase
go run ./cmd/showcase
```

Open [http://localhost:3333](http://localhost:3333) to browse all components.

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│  dsx                                                         │
│                                                              │
│  ui/           70+ DaisyUI components (templ)                │
│  ds/           Type-safe Datastar helpers (frontend + SSE)   │
│  stream/       Reactive SSE relay backed by pub/sub          │
│  layouts/      Base HTML + Dashboard layout                  │
│  utils/        TwMerge, If, RandomID                         │
│  showcase/     Reusable dev server with identity switching   │
│                                                              │
│  External:                                                   │
│  pubsub        Pub/sub interface + adapters (NATS/Redis/Chan)│
│  identity      Multi-tenant identity context                 │
└──────────────────────────────────────────────────────────────┘
```

## Core packages

### `dsx` — Context and middleware

Every request carries a `dsx.Context` with session, CSRF, theme, and stream state.

```go
import "github.com/laenen-partners/dsx"

r := chi.NewRouter()

// Session + CSRF middleware (cookie-based, no session store needed)
r.Use(dsx.Middleware(dsx.MiddlewareConfig{
    Secret: secret, // 32-byte HMAC key
    Secure: true,   // HTTPS-only cookies
}))
r.Use(dsx.SecurityHeadersMiddleware())
```

Access in handlers:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    ctx := dsx.FromContext(r.Context())
    ctx.SessionID  // unique per browser
    ctx.CSRFToken  // signed double-submit token
    ctx.Theme      // current DaisyUI theme
    ctx.BasePath   // e.g. "/app"
    ctx.APIPath("/customers/list") // → "/app/customers/list"
}
```

### `ds` — Datastar helpers

Type-safe helpers for Datastar attributes and SSE operations. Prevents common mistakes like `data-on-click` (wrong) vs `data-on:click` (correct).

#### Frontend attributes

```go
import "github.com/laenen-partners/dsx/ds"

// Event handlers
ds.OnClick(ds.Post("/api/save"))     // data-on:click="@post('/api/save', ...)"
ds.On("keydown", "$value = ''")      // data-on:keydown="$value = ''"

// Data binding
ds.Bind("form1", "email")            // data-bind:form1.email

// Display
ds.Show("$isVisible")                // data-show="$isVisible"
ds.Text("$count + ' items'")         // data-text="$count + ' items'"
ds.ClassToggle("active", "$isOn")    // data-class:active="$isOn"

// Initialization
ds.Init(ds.GetOnce("/api/data"))      // data-init="@get('/api/data')"

// Merge multiple attribute maps
ds.Merge(ds.OnClick(expr), ds.Show("$open"))
```

#### SSE backend operations

```go
// Read form signals from request
var signals MyForm
ds.ReadSignals("form-id", r, &signals)

// Patch a templ component into the DOM
ds.Send.Patch(sse, myComponent(data))

// UI feedback
ds.Send.Toast(sse, ds.ToastSuccess, "Saved!")
ds.Send.Drawer(sse, editForm(item))
ds.Send.Modal(sse, confirmDialog())
ds.Send.Confirm(sse, "Delete this?", "/api/delete/42")
ds.Send.HideDrawer(sse)
ds.Send.HideModal(sse)
ds.Send.Redirect(sse, "/dashboard")
ds.Send.Download(sse, "/files/report.pdf", "report.pdf")
```

### `stream` — Reactive SSE streaming

Real-time updates across all browser tabs. When data changes, all connected clients refresh automatically.

```
Tab A (editor)              Server                    Tab B (viewer)
  |                           |                           |
  | POST /save               |                           |
  |------------------------->|                           |
  |                           | bus.NotifyUpdated(ctx,   |
  |                           |   "invoice", "42")       |
  |                           |---------> Pub/Sub        |
  |                           |                           |
  |                           | <-- stale signal -------->|
  |  _stream.invoice_42=true  |  _stream.invoice_42=true |
  |                           |                           |
  | GET /api/invoice/42       |   GET /api/invoice/42     |
  |------------------------->|<--------------------------|
  | <-- fresh HTML            |      fresh HTML --------->|
```

#### In components (templ)

```go
import "github.com/laenen-partners/dsx/stream"

// Register a scope — component reloads when scope goes stale
templ InvoiceCard(invoice Invoice) {
    <div { stream.Attrs(ctx, "invoice:42", "/api/invoice/42")... }>
        // component content
    </div>
}

// Or use WatchEffect for more control
templ CustomerList() {
    <div data-effect={ stream.WatchEffect(ctx, "customers:*", "/api/customers") }>
        // ...
    </div>
}
```

#### In handlers (publish changes)

```go
import "github.com/laenen-partners/pubsub"

// After saving data, notify all watchers
func (h *handler) save(w http.ResponseWriter, r *http.Request) {
    // ... save to database ...

    // All browsers watching "invoice:42" will refresh
    h.bus.NotifyUpdated(r.Context(), "invoice", "42")

    datastar.NewSSE(w, r) // close the SSE response
}
```

#### Wiring

```go
import (
    "github.com/laenen-partners/dsx/stream"
    "github.com/laenen-partners/pubsub"
    "github.com/laenen-partners/pubsub/chanpubsub" // or natspubsub, redispubsub
)

ps := chanpubsub.New()
bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope("tenant1", "workspace1"))
relay := stream.New(ps)

r.Get("/stream", relay.Handler())
```

#### Scope naming

Scopes use `entity:id` format and map to pub/sub change topics:

| Scope | Subscribes to | Use case |
|-------|--------------|----------|
| `invoice:42` | `change.invoice.42.>` | Specific entity |
| `invoices:*` | `change.invoices.*.*` | All invoices |
| `customer:>` | `change.customer.>` | All customer changes |

#### Pub/sub adapters

| Adapter | Package | Use case |
|---------|---------|----------|
| Go channels | `pubsub/chanpubsub` | Development, testing |
| NATS | `pubsub/natspubsub` | Production (recommended) |
| Redis | `pubsub/redispubsub` | Production (alternative) |

### `layouts` — Page layouts

```go
import "github.com/laenen-partners/dsx/layouts"

// Base layout provides HTML shell with all required containers
templ MyPage() {
    @layouts.Base(layouts.BaseProps{
        Title:     "My App",
        Theme:     "silk",
        CSRFToken: dsxCtx.CSRFToken,
        Head:      myHead(),
    }) {
        // page content
    }
}
```

The Base layout includes containers for SSE-driven UI:

```
<body>
    { children }              ← your page content
    <div id="drawer-panel">   ← ds.Send.Drawer() target
    <div id="modal-panel">    ← ds.Send.Modal() / Confirm() target
    @stream.Connect()         ← opens SSE stream for registered scopes
    <div id="toast-container"> ← ds.Send.Toast() target
</body>
```

The Dashboard layout adds a sidebar, navbar, and optional detail panel:

```go
@layouts.Dashboard(layouts.DashboardProps{
    BaseProps: baseProps,
    App:       layouts.AppBranding{Name: "MyApp", Href: "/"},
    Nav:       navGroups,
    CurrentPath: r.URL.Path,
    ThemeToggle: &layouts.ThemeToggleConfig{
        DarkTheme: "dark", LightTheme: "silk",
    },
})
```

## Components

70+ components in `ui/`, each following the same pattern:

```go
import "github.com/laenen-partners/dsx/ui/button"

@button.Button(button.Props{
    Variant: button.VariantPrimary,
    Size:    button.SizeLg,
    OnClick: ds.Post("/api/action"),
}) {
    Save
}
```

All components:
- Use DaisyUI CSS classes for styling
- Accept optional variadic `Props` with sensible defaults
- Support `Class` for extra Tailwind classes (merged via `TwMerge`)
- Support `Attributes` for arbitrary HTML attributes
- Use theme tokens, never hardcoded colors

Interactive components (calendar, form, file upload, etc.) include `handler.go` with SSE endpoints registered via `ui.RegisterRoutes()`.

## Showcase server

The `showcase` package provides a reusable dev server for previewing components:

```go
import (
    "github.com/laenen-partners/dsx/showcase"
    "github.com/laenen-partners/pubsub"
    "github.com/laenen-partners/dsx/stream"
)

showcase.Run(showcase.Config{
    Port: 3333,
    Identities: []showcase.Identity{
        {Name: "Admin", TenantID: "t1", PrincipalID: "admin-1", Roles: []string{"admin"}},
        {Name: "Viewer", TenantID: "t1", PrincipalID: "viewer-1", Roles: []string{"viewer"}},
    },
    Pages: map[string]templ.Component{
        "/": homePage(),
    },
    Setup: func(ctx context.Context, r chi.Router, bus *pubsub.Bus, relay *stream.Relay) error {
        // register fragment routes
        return nil
    },
})
```

Features:
- In-process pub/sub (zero external deps)
- Identity switching with role-based testing
- Context editor for theme, tenant, workspace
- CSRF + security headers pre-configured
- `PORT` env var support (e.g. `PORT=0` for random port)

## Best practices

### Component design

```go
// DO: Use optional variadic props with zero-value defaults
templ MyComponent(props ...Props) {
    {{ var p Props }}
    if len(props) > 0 {
        {{ p = props[0] }}
    }
    // ...
}

// DO: Use TwMerge for class composition
class := utils.TwMerge("btn btn-primary", p.Class)

// DO: Use theme tokens
"bg-base-200 text-base-content border-base-300"

// DON'T: Hardcode colors
"bg-gray-100 text-gray-900 border-gray-300"
```

### SSE handlers

```go
// DO: Use ds.ReadSignals for type-safe form handling
var signals MyForm
if err := ds.ReadSignals("form-id", r, &signals); err != nil { ... }

// DO: Close SSE response on mutation handlers
func increment(w http.ResponseWriter, r *http.Request) {
    counter.Add(1)
    bus.NotifyUpdated(r.Context(), "counter", "shared")
    datastar.NewSSE(w, r) // important: closes the SSE cleanly
}

// DON'T: Send PatchElements without a target element
// (causes browser error when no matching ID exists)
```

### Stream scopes

```go
// DO: Use entity:id format matching your domain
stream.WatchEffect(ctx, "customer:42", "/api/customers/42")
stream.WatchEffect(ctx, "customers:*", "/api/customers")

// DO: Use PreRegister for async-loaded fragments
stream.PreRegister(ctx, "notifications:*")

// DO: Use bus.NotifyCreated/Updated/Deleted for semantic notifications
bus.NotifyCreated(ctx, "customer", "42")
bus.NotifyUpdated(ctx, "invoice", "123")
bus.NotifyDeleted(ctx, "order", "99")
```

### Security

```go
// DO: Always use dsx.Middleware for CSRF protection
r.Use(dsx.Middleware(dsx.MiddlewareConfig{
    Secret: secret,
    Secure: true, // set true in production
}))

// DO: Add security headers
r.Use(dsx.SecurityHeadersMiddleware())

// ds.Post/Put/Delete automatically include X-CSRF-Token header
ds.OnClick(ds.Post("/api/save"))
```

## Project structure

```
dsx/
  ui/                   70+ DaisyUI components
    button/
      button.templ      component template
      button_templ.go   generated (do not edit)
    form/
      form.templ        component template
      handler.go        SSE handler
      routes.go         route registration
  ds/                   Datastar helpers
    ds.go               frontend attributes
    signals.go          signal reading
    send*.go            SSE operations (toast, drawer, modal, etc.)
  stream/               reactive SSE streaming
    stream.go           Relay, scopes, watchers
    connect.templ       SSE connection component
  layouts/              Base + Dashboard layouts
  utils/                TwMerge, If, RandomID
  showcase/             reusable dev server
  cmd/showcase/         main dsx component gallery
  docs/                 reference documentation
  static/css/           Tailwind CSS + DaisyUI
```

## Commands

```bash
go tool templ generate       # Generate Go from .templ files
go tool templ fmt .          # Format .templ files
go tool gotailwind           # Build Tailwind CSS
go tool task install:all     # Install all dependencies
go build ./...               # Build everything
go test ./...                # Run all tests
go run ./cmd/showcase        # Run the component showcase
```

## License

See [LICENSE](LICENSE).
