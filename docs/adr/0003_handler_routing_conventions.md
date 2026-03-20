# ADR-0003: Handler & Routing Conventions

## Status

Proposed

## Context

A WebX application has several distinct types of HTTP endpoints:

| Type | Verb | Returns | Example |
|------|------|---------|---------|
| **Page** | GET | Full HTML document (layout + content) | `/customers`, `/invoices/42` |
| **Fragment** | GET | SSE `patch-elements` (partial DOM) | Fragment reload after stream invalidation, initial data load |
| **Mutation** | POST/PUT/PATCH/DELETE | SSE signals, toasts, drawer/modal control | Form submit, button action |
| **Validator** | GET/POST | SSE `patch-signals` (field errors) | Live field validation |
| **Stream** | GET | Persistent SSE (stale signals) | `/stream` |

As the application grows, the number of endpoints scales roughly as:

```
pages + (fragments per page * pages) + mutations + validators
```

A modest CRUD app with 10 entities easily reaches 50-100 endpoints. Without a consistent convention, the route file becomes an unnavigable wall of registrations and the handler directory a flat soup of files.

### Current state

The showcase app mixes two patterns:

1. **Flat handler files** grouped loosely by feature (`customer.go`, `form.go`, `modal.go`) — each registers its own routes via `register(r chi.Router)` on a shared `Handlers` struct
2. **UI component routes** exported as `Route()` functions and registered centrally via `ui.RegisterRoutes(r, ...)`

This works at showcase scale but has emerging issues:

- **Route discovery**: Finding which handler serves `/customers/list` requires grepping across multiple files
- **Naming collisions**: Two features that both want `/list` would collide in a flat namespace
- **Mixed concerns**: Pages (full HTML) and fragments (SSE partials) are registered on the same router with no visual distinction
- **No convention for validators**: Validator endpoints are ad-hoc — some under `/validate/*`, some under `/form/*`

## Decision

### 1. Resource-scoped route groups

Every resource (entity, feature) gets its own Chi `r.Route` group. All endpoints for that resource — pages, fragments, mutations, validators — live under one URL prefix and one handler file.

```go
r.Route("/customers", func(r chi.Router) {
    r.Get("/",           h.customers.page())        // page
    r.Get("/list",       h.customers.list())        // fragment
    r.Get("/count",      h.customers.count())       // fragment
    r.Get("/new",        h.customers.newDrawer())    // fragment (drawer)
    r.Post("/create",    h.customers.create())       // mutation
    r.Put("/{id}",       h.customers.update())       // mutation
    r.Delete("/{id}",    h.customers.delete())       // mutation
})
```

**Why**: Colocation. Every endpoint for `customers` is visible in one place. Chi's `r.Route` provides namespace isolation — `/customers/list` can't collide with `/invoices/list`. Middleware can be scoped per resource (e.g. admin-only on `/users`).

### 2. Endpoint type conventions

Distinguish endpoint types by HTTP verb and path suffix:

| Convention | Verb | Path pattern | Returns |
|------------|------|-------------|---------|
| Page | GET | `/` or `/{id}` | Full HTML |
| Fragment | GET | `/list`, `/count`, `/detail`, `/new`, `/edit` | SSE `patch-elements` |
| Mutation | POST/PUT/PATCH/DELETE | `/create`, `/{id}`, `/batch` | SSE signals + side effects |
| Validator | POST | `/validate/{field}` | SSE `patch-signals` |

Fragments are **always GET** — they render and return HTML. They never mutate state.

Mutations are **never GET** — they change state and return SSE control signals (close drawer, show toast, invalidate).

This verb-based split means:

- Browsers can't accidentally mutate via URL bar / prefetch
- Middleware can enforce CSRF only on non-GET (already the case)
- Caching and retry semantics are correct by default

### 3. Handler struct per resource

Each resource gets its own handler struct in its own file:

```
cmd/myapp/internal/handlers/
├── handlers.go          # top-level Handlers, New(), RegisterRoutes()
├── customers.go         # customerHandlers struct + all customer endpoints
├── invoices.go          # invoiceHandlers struct + all invoice endpoints
└── dashboard.go         # dashboardHandlers struct
```

The top-level `Handlers` struct owns and wires them:

```go
type Handlers struct {
    customers *customerHandlers
    invoices  *invoiceHandlers
    dashboard *dashboardHandlers
}

func (h *Handlers) RegisterRoutes(r chi.Router) {
    r.Route("/customers", h.customers.register)
    r.Route("/invoices",  h.invoices.register)
    r.Route("/dashboard", h.dashboard.register)
}
```

Each resource handler receives only the dependencies it needs:

```go
type customerHandlers struct {
    store  CustomerStore
    bus *pubsub.Bus
}

func (h *customerHandlers) register(r chi.Router) {
    r.Get("/",        h.page())
    r.Get("/list",    h.list())
    r.Get("/count",   h.count())
    r.Get("/new",     h.newDrawer())
    r.Post("/create", h.create())
}
```

**Why**: Each file is self-contained. Adding a new resource means adding one file and one line in `RegisterRoutes`. Dependencies are explicit per resource, not a god-struct shared by everyone.

### 4. Page vs. API route separation

Pages (full HTML responses) and API endpoints (SSE fragments/mutations) have different middleware needs. Pages need layout rendering, HTML content-type, and often different caching. API endpoints need SSE headers and Datastar-specific handling.

Two approaches work, choose one per app:

**Option A: Unified (small apps)** — Pages and fragments live in the same resource group. The handler method decides what to return:

```go
r.Route("/customers", func(r chi.Router) {
    r.Get("/",        h.customers.page())    // returns full HTML
    r.Get("/list",    h.customers.list())    // returns SSE fragment
    r.Post("/create", h.customers.create())  // returns SSE mutation
})
```

**Option B: Split (large apps)** — Pages on one router, API on another with different middleware:

```go
// Pages — HTML responses, may have different auth/caching
r.Get("/customers",     h.customers.page())
r.Get("/invoices",      h.invoices.page())
r.Get("/invoices/{id}", h.invoices.detail())

// API — SSE responses, CSRF enforcement, JSON signal reading
r.Route("/api", func(r chi.Router) {
    r.Route("/customers", h.customers.register)
    r.Route("/invoices",  h.invoices.register)
})
```

**Why the split matters**: In the split model, `dsx.Context.APIPath()` already prefixes API paths with the base path. Page routes use human-readable URLs (`/customers`). API routes use a programmatic namespace (`/api/customers/list`). This separation makes it easy to add API-specific middleware (rate limiting, auth scoping) without affecting page routes.

### 5. Validator endpoints under the resource

Validators belong to the resource they validate, not a global `/validate/*` namespace:

```go
r.Route("/customers", func(r chi.Router) {
    // ... other endpoints ...
    r.Post("/validate/email",  h.customers.validateEmail())
    r.Post("/validate/phone",  h.customers.validatePhone())
})
```

For **reusable validators** (email format, IBAN, phone) that aren't resource-specific, the existing `ui/validator` package with `validator.Route()` is the right home — these are library-level, not app-level.

**Why**: Resource-specific validation (e.g. "is this email already taken?") needs access to the resource's store. Keeping it in the resource handler avoids cross-cutting dependencies. Generic format validators stay in the UI library.

### 6. Naming conventions for handler methods

Handler methods follow a consistent naming pattern:

| Method name | Returns | Purpose |
|-------------|---------|---------|
| `page()` | Full HTML | Render the resource's page |
| `list()` | SSE fragment | Render a collection/table |
| `count()` | SSE fragment | Render a count/stat |
| `detail()` | SSE fragment | Render a single item |
| `newDrawer()` / `newModal()` | SSE drawer/modal | Open a creation form |
| `editDrawer()` / `editModal()` | SSE drawer/modal | Open an edit form |
| `create()` | SSE mutation | Handle creation |
| `update()` | SSE mutation | Handle update |
| `delete()` | SSE mutation | Handle deletion |
| `validateX()` | SSE signals | Validate field X |

All methods return `http.HandlerFunc`. The naming makes it obvious what each endpoint does when reading `register()`.

### 7. Scaling pattern: nested resources

For nested resources (e.g. invoice line items), nest the route groups:

```go
r.Route("/invoices", func(r chi.Router) {
    r.Get("/",          h.invoices.page())
    r.Get("/list",      h.invoices.list())
    r.Route("/{id}", func(r chi.Router) {
        r.Get("/",      h.invoices.detail())
        r.Put("/",      h.invoices.update())
        r.Route("/lines", func(r chi.Router) {
            r.Get("/list",    h.invoiceLines.list())
            r.Post("/create", h.invoiceLines.create())
        })
    })
})
```

This keeps URLs predictable (`/invoices/42/lines/list`) and middleware can scope to the parent resource (e.g. access control on `/{id}`).

### Summary

```
┌─────────────────────────────────────────────────────────┐
│ RegisterRoutes(r)                                       │
│                                                         │
│   r.Route("/customers", customers.register)             │
│       GET  /              → page (full HTML)            │
│       GET  /list          → fragment (SSE patch)        │
│       GET  /count         → fragment (SSE patch)        │
│       GET  /new           → fragment (drawer)           │
│       POST /create        → mutation (SSE signals)      │
│       POST /validate/email→ validator (SSE signals)     │
│       PUT  /{id}          → mutation                    │
│       DELETE /{id}        → mutation                    │
│                                                         │
│   r.Route("/invoices", invoices.register)               │
│       GET  /              → page                        │
│       GET  /list          → fragment                    │
│       ...                                               │
│       r.Route("/{id}/lines", invoiceLines.register)     │
│           GET  /list      → fragment                    │
│           POST /create    → mutation                    │
│                                                         │
│   ui.RegisterRoutes(r,                                  │
│       calendar.Route(),    // library-level handlers    │
│       validator.Route(),   // reusable validators       │
│   )                                                     │
│                                                         │
│   r.Get("/stream",           relay.Handler())          │
└─────────────────────────────────────────────────────────┘
```

## Consequences

### Positive

- **Discoverable**: All endpoints for a resource are in one `register()` method and one file
- **Isolated**: Resources can't collide on paths; middleware can be scoped per resource
- **Predictable**: Verb and path conventions make it obvious what an endpoint does without reading the implementation
- **Scalable**: Adding a resource is one file + one line in `RegisterRoutes` — no coordination with other resources
- **Testable**: Each resource handler struct can be instantiated independently with mock dependencies

### Negative

- **More files**: Each resource gets its own file even if it only has 2-3 endpoints
- **Nesting depth**: Deeply nested resources (`/org/1/team/2/member/3/settings`) produce deep route trees — consider flattening when nesting exceeds 3 levels
- **Convention enforcement**: These are conventions, not compiler-enforced rules — code review must catch violations

### Trade-offs

- **Flat vs. nested handlers**: A flat `handlers/` directory works up to ~15 resources. Beyond that, consider subdirectories per domain (`handlers/billing/`, `handlers/admin/`). The registration pattern stays the same.
- **Page in resource vs. separate**: Keeping pages in the resource group is simpler but means page middleware applies to fragments too. The split model (Option B) adds a routing layer but gives cleaner middleware separation.
- **Generic vs. resource validators**: Format-only validators (email regex, IBAN checksum) belong in the UI library. Business validators (unique email check, credit limit validation) belong in the resource handler. When in doubt, put it in the resource — it's easy to extract later, hard to merge back.
