---
name: build-frontend
description: Build state-of-the-art frontends with the DSX package — covers architecture, components, forms, validation, live updates, drawers, modals, toasts, file uploads, and all best practices
---

Build a frontend feature using the DSX package (Go + Templ + DaisyUI + Datastar).

Follow the architecture, patterns, and rules below exactly. This skill is self-contained — all Datastar reference material is included in the appendices at the end:

- **Appendix A** — Datastar + Go Backend: Comprehensive Reference (Go SDK, SSE protocol, patterns)
- **Appendix B** — HTML Elements & Datastar Interaction Reference (every HTML element, events, bindings)

---

## 1. Architecture Overview

DSX is a **server-driven UI framework**. The browser never fetches JSON APIs — instead, Go handlers respond with **Server-Sent Events (SSE)** that patch HTML fragments and reactive signals directly into the DOM. There is no separate REST layer and no client-side JavaScript to write.

**Stack**: Go + Chi (routing) + Templ (type-safe HTML) + Tailwind CSS + DaisyUI (styling) + Datastar (frontend reactivity via `data-*` attributes)

**Request flow**:
```
Browser                              Go Backend
  │                                      │
  │ ── @post('/api/submit') ──────────►  │  (signals sent as JSON body)
  │                                      │
  │ ◄── text/event-stream ────────────  │  (SSE: patch-elements, patch-signals)
  │                                      │
  │  [Datastar morphs DOM & updates     │
  │   signals reactively — no JS]       │
```

---

## 2. Project Structure

```
ui/                  — Reusable UI components (one dir per component)
ds/                  — Typed Datastar helpers (attributes, signals, SSE senders)
stream/              — Real-time SSE subscriptions backed by pub/sub
utils/               — Shared utilities (TwMerge, If, RandomID)
layouts/             — Base HTML layouts
cmd/                 — Application entry points
static/css/          — Tailwind + DaisyUI plugin files
static/js/           — Datastar JS bundle + watch worker
```

**Imports**:
```go
import (
    "github.com/laenen-partners/dsx"                // Context, Middleware
    "github.com/laenen-partners/dsx/ds"              // Datastar helpers
    "github.com/laenen-partners/dsx/stream"          // Live updates
    "github.com/laenen-partners/dsx/utils"           // TwMerge, If, RandomID
    "github.com/laenen-partners/dsx/ui/button"       // UI components
    "github.com/laenen-partners/dsx/ui/form"         // Form components + handler
    "github.com/starfederation/datastar-go/datastar" // Datastar Go SDK
)
```

---

## 3. Component Pattern

Every UI component follows this exact pattern:

```go
package mycomponent

import "github.com/laenen-partners/dsx/utils"

type Props struct {
    ID         string
    Class      string
    Attributes templ.Attributes
    // Component-specific: Variant, Size, etc.
}

templ MyComponent(props ...Props) {
    {{ var p Props }}
    if len(props) > 0 {
        {{ p = props[0] }}
    }
    <div
        class={ utils.TwMerge("base-daisyui-classes", string(p.Variant), string(p.Size), p.Class) }
        { p.Attributes... }
    >
        { children... }
    </div>
}
```

**Rules**:
- Props use variadic `...Props` so callers can omit them entirely
- Always use `utils.TwMerge()` to merge DaisyUI base classes with user overrides
- Variant and Size are typed string constants (e.g. `type Variant string`)
- Use DaisyUI classes exclusively — never write custom CSS

---

## 4. The `ds` Package — Typed Datastar Helpers

**CRITICAL**: Never write raw `data-on-click` or `data-bind` attributes. Datastar uses colon syntax (`data-on:click`), and a hyphen is silently ignored. The `ds` package makes this mistake impossible.

### Frontend Attributes (templ-side)

```go
// Event handlers
{ ds.OnClick(expr)... }                          // data-on:click="expr"
{ ds.On("input__debounce_300", expr)... }        // data-on:input.debounce.300="expr"
{ ds.On("submit__prevent", expr)... }            // data-on:submit.prevent="expr"

// Two-way binding (connects input ↔ signal)
{ ds.Bind("form-id", "fieldname")... }           // data-bind:form_id.fieldname=""

// Conditional rendering
{ ds.Show("$signal.visible")... }                // data-show="$signal.visible"
{ ds.ClassToggle("hidden", "$s.err === ''")... } // data-class:hidden="$s.err === ''"
{ ds.Text("$signal.label")... }                  // data-text="$signal.label"

// Reactive attributes
{ ds.Attr("disabled", "$form.submitting")... }   // data-attr:disabled="$form.submitting"
{ ds.Effect("console.log($signal.value)")... }   // data-effect="..."
{ ds.Init(ds.GetOnce("/api/load"))... }          // data-init="@get('/api/load', ...)"

// Dynamic CSS classes (multiple)
data-class={ ds.NewDataClass().
    Add("btn-active", signals.Equals("tab", "home")).
    Add("btn-ghost", signals.NotEquals("tab", "home")).
    Build() }

// Combine multiple attributes
{ ds.Merge(ds.OnClick(expr), ds.Attr("disabled", cond))... }
```

### Backend Action Expressions

```go
// GET — no CSRF needed
ds.Get("/api/data")                                // @get('/api/data')
ds.GetOnce("/api/data")                            // @get('/api/data', {retryMaxCount: 0})
ds.Get("/api/data", ds.WithRetries(3))             // @get('/api/data', {retryMaxCount: 3})

// POST/PUT/PATCH/DELETE — CSRF header included automatically
ds.Post("/api/submit")                             // @post('/api/submit', {headers: {'X-CSRF-Token': ...}})
ds.PostOnce("/api/submit")                         // Single shot, no retries
ds.Post("/api/upload", ds.WithContentType("form")) // multipart/form-data
ds.Post("/api/save", ds.WithFilterSignals("my-form")) // Only send this form's signals

// All methods: Post, Put, Patch, Delete (and *Once variants)
```

### Signal Management

```go
// Define signal struct with json tags
type MySignals struct {
    Search string `json:"search"`
    Open   bool   `json:"open"`
}

// Create namespaced signals
signals := ds.NewSignals("component-id", MySignals{Search: "", Open: false})

// Use in templates
data-signals={ signals.DataSignals }          // Initializes signals on DOM element
signals.Signal("search")                      // → "$component_id.search"
signals.Toggle("open")                        // → "$component_id.open = !$component_id.open"
signals.Set("search", "'hello'")              // → "$component_id.search = 'hello'"
signals.SetString("search", "hello")          // → "$component_id.search = 'hello'" (quoted)
signals.Equals("open", "true")                // → "$component_id.open === 'true'"
signals.Conditional("open", "'Yes'", "'No'")  // → "$component_id.open ? 'Yes' : 'No'"
```

### Reading Signals Server-Side

```go
// IMPORTANT: Call ReadSignals BEFORE datastar.NewSSE() — SSE creation consumes the body.
var signals MySignals
if err := ds.ReadSignals("component-id", r, &signals); err != nil {
    // handle error
}

// Or read + create SSE in one call:
sse, err := ds.ReadAndSSE("component-id", w, r, &signals)
```

---

## 5. Forms — Complete Pattern

Forms are the most common interactive pattern. Follow this exactly.

### Step 1: Define Signal Structs

```go
// In your page .templ file — includes error + success signals for the UI
type loginSignals struct {
    Email         string `json:"email"`
    Password      string `json:"password"`
    EmailError    string `json:"email_error"`
    PasswordError string `json:"password_error"`
    Success       string `json:"success"`
}

// In your handler .go file — only the data fields
type loginFormSignals struct {
    Email    string `json:"email"`
    Password string `json:"password"`
}
```

**Why two structs?** The templ-side struct includes UI-only signals (errors, success). The handler-side struct only has the fields it needs to read. The `form.Handler` derives `_error` suffixes automatically from the handler struct's json tags.

### Step 2: Build the Templ Form

```go
@form.Form(form.Props{
    ID:      "login",
    Action:  wxctx.APIPath("/auth/login"),  // Always use APIPath for correct base path
    Signals: loginSignals{},
}) {
    @form.Field() {
        @form.Label() { Email }
        <input
            type="email"
            class="input w-full"
            placeholder="email@example.com"
            { ds.Bind("login", "email")... }
        />
        @form.Error("$login.email_error")
    }
    @form.Field() {
        @form.Label() { Password }
        <input
            type="password"
            class="input w-full"
            { ds.Bind("login", "password")... }
        />
        @form.Error("$login.password_error")
    }
    @form.Submit(form.SubmitProps{
        FormID:  "login",
        Variant: button.VariantPrimary,
    }) {
        Sign In
    }
    @form.Success("$login.success")
}
```

**Key details**:
- `ds.Bind("login", "email")` binds the input to signal `$login.email`
- `form.Error("$login.email_error")` auto-hides when the error signal is empty
- `form.Submit` shows a loading spinner while `submitting` signal is true
- Form uses `data-on:submit.prevent` — no page reload, submits via SSE
- `novalidate` is set — validation is 100% server-side

### Step 3: Write the Handler

```go
func (f *formHandlers) login() http.HandlerFunc {
    return form.Handler(
        loginFormSignals{},  // Signal struct — error fields derived from json tags
        func(formID string, r *http.Request) []form.FieldError {
            // 1. Read signals from request
            var signals loginFormSignals
            if err := ds.ReadSignals(formID, r, &signals); err != nil {
                return []form.FieldError{{Field: "error", Message: "Failed to read form data"}}
            }

            // 2. Validate each field
            var errs []form.FieldError

            if signals.Email == "" {
                errs = append(errs, form.FieldError{
                    Field:   "email_error",
                    Message: "Email is required",
                })
            } else {
                res := validators.Email(signals.Email, false)
                if !res.Valid {
                    errs = append(errs, form.FieldError{
                        Field:   "email_error",
                        Message: res.Error,
                    })
                }
            }

            if signals.Password == "" {
                errs = append(errs, form.FieldError{
                    Field:   "password_error",
                    Message: "Password is required",
                })
            } else if len(signals.Password) < 8 {
                errs = append(errs, form.FieldError{
                    Field:   "password_error",
                    Message: "Password must be at least 8 characters",
                })
            }

            return errs  // nil = success
        },
        func(formID string, sse *datastar.ServerSentEventGenerator) {
            // 3. Success callback — update signals or redirect
            sanitizedID := strings.ReplaceAll(formID, "-", "_")
            _ = sse.MarshalAndPatchSignals(map[string]any{
                sanitizedID: map[string]any{
                    "success": "Login successful!",
                },
            })
        },
    )
}
```

### Step 4: Register the Route

```go
func (f *formHandlers) register(r chi.Router) {
    r.Post("/auth/login", f.login())
}
```

### Validation Rules

- **Field errors**: Use `Field: "fieldname_error"` — the handler auto-clears all `_error` fields before applying new ones
- **Form-level errors (toast)**: Use `Field: "error"` — this triggers a toast notification instead of inline display
- **Always read signals first**: Call `ds.ReadSignals()` before any SSE creation
- **Validate server-side only**: The form sets `novalidate` — never rely on browser validation
- **Use `validators` package**: `validators.Email(email, checkDNS)` for email validation

### Form Props Options

```go
form.Props{
    ID:        "my-form",           // Required, unique
    Action:    "/api/endpoint",     // Backend handler URL
    Method:    "put",               // "post" (default), "put", "patch", "delete"
    Multipart: true,                // For file uploads (adds enctype + contentType: 'form')
    Signals:   MySignals{},         // Initial signal state
    Class:     "max-w-lg",          // Additional CSS
}
```

---

## 6. SSE Operations — `ds.Send.*`

All server-to-browser communication goes through the `ds.Send` namespace.

### Toast Notifications

```go
sse := datastar.NewSSE(w, r)

// Basic toasts (auto-dismiss after 3s)
_ = ds.Send.Toast(sse, ds.ToastSuccess, "Saved!")
_ = ds.Send.Toast(sse, ds.ToastError, "Something went wrong")
_ = ds.Send.Toast(sse, ds.ToastWarning, "Check your input")
_ = ds.Send.Toast(sse, ds.ToastInfo, "Processing...")

// Persistent toast (stays until closed)
_ = ds.Send.Toast(sse, ds.ToastInfo, "Background job running", ds.WithToastPersistent())

// Custom duration
_ = ds.Send.Toast(sse, ds.ToastSuccess, "Done!", ds.WithToastDuration(5000))

// With action button (triggers @get on click, auto-persistent)
_ = ds.Send.Toast(sse, ds.ToastInfo, "Item deleted",
    ds.WithToastAction("Undo", wxctx.APIPath("/undo")))

// With link
_ = ds.Send.Toast(sse, ds.ToastSuccess, "Report ready",
    ds.WithToastLink("View", "/reports/123"))
```

### Drawer (Slide-in Panel)

```go
// Open a drawer with templ content
_ = ds.Send.Drawer(r.Context(), sse, pages.EditForm(data), ds.WithDrawerExpandable())

// Options
ds.WithDrawerMaxWidth("max-w-2xl")  // Default: "max-w-lg"
ds.WithDrawerExpandable()            // Adds expand/collapse toggle

// Close drawer (e.g., after form submit in drawer)
_ = ds.Send.HideDrawer(sse)
```

### Modal Dialog

```go
// Open a modal
_ = ds.Send.Modal(r.Context(), sse, pages.DetailView(data))
_ = ds.Send.Modal(r.Context(), sse, pages.DetailView(data), ds.WithModalMaxWidth("max-w-2xl"))

// Close modal
_ = ds.Send.HideModal(sse)
```

### Confirm Dialog

```go
_ = ds.Send.Confirm(sse, "Are you sure you want to delete this?",
    wxctx.APIPath("/items/delete"),
    ds.WithConfirmTitle("Delete Item"),
    ds.WithConfirmLabel("Delete"),
    ds.WithConfirmClass("btn btn-error"),
)
// Default method is POST with CSRF. Use ds.WithConfirmGet() for GET.
```

### Patch DOM Elements

```go
// Patch a templ component into the DOM (root element must have id)
_ = ds.Send.Patch(sse, pages.CustomerTableBody(customers))

// Patch raw HTML
_ = sse.PatchElements(`<div id="counter">42</div>`)
```

### Other Operations

```go
_ = ds.Send.Redirect(sse, "/dashboard")                    // Browser navigation
_ = ds.Send.Download(sse, "/files/report.pdf", "report.pdf") // File download
_ = sse.ExecuteScript("console.log('done')")                // Run JS (use sparingly)
_ = sse.MarshalAndPatchSignals(map[string]any{...})         // Update signals directly
```

---

## 7. Live Updates — `stream.Watch`

For real-time multi-tab/multi-user updates without polling.

### Templ Side — Declare Subscriptions

```go
{{ wxctx := dsx.FromContext(ctx) }}

// Watch for structural changes (items added/removed) — reloads the list
<div { stream.Watch(ctx, "customers",
    stream.Structural.Get(wxctx.APIPath("/customers/list")))... }>
    <table>
        <tbody id="customer-table-body"
            { ds.Init(ds.GetOnce(wxctx.APIPath("/customers/list")))... }>
            Loading...
        </tbody>
    </table>
</div>

// Watch for any change — reloads the count
<div { stream.Watch(ctx, "customers",
    stream.Any.Get(wxctx.APIPath("/customers/count")))... }>
    <div id="customer-count"
        { ds.Init(ds.GetOnce(wxctx.APIPath("/customers/count")))... }>
        —
    </div>
</div>

// Watch for updates to a specific entity
<div { stream.Watch(ctx, "customers",
    stream.Updated.ID(42).Get(wxctx.APIPath("/customers/42/row")))... }>
    ...
</div>

// Debounce rapid events (e.g., bulk operations)
<div { stream.Watch(ctx, "customers",
    stream.Structural.Debounce(300*time.Millisecond).Get(url))... }>
    ...
</div>
```

### Action Sets

| Set | Matches | Use For |
|-----|---------|---------|
| `stream.Created` | New items | Add-to-list |
| `stream.Updated` | Modified items | Row refresh |
| `stream.Deleted` | Removed items | Remove-from-list |
| `stream.Structural` | Created + Deleted | Tables, lists |
| `stream.Any` | All actions | Counters, dashboards |
| `stream.Action("custom")` | Custom events | App-specific |

Combine with `.Or()`: `stream.Updated.Or(stream.Action("archived")).Get(url)`

### Handler Side — Publish Notifications

```go
// After creating a customer:
if err := h.bus.NotifyCreated(r.Context(), "customers", strconv.Itoa(id)); err != nil {
    return []form.FieldError{{Field: "error", Message: fmt.Sprintf("Notification failed: %v", err)}}
}

// After updating:
_ = h.bus.NotifyUpdated(r.Context(), "customers", strconv.Itoa(id))

// After deleting:
_ = h.bus.NotifyDeleted(r.Context(), "customers", strconv.Itoa(id))
```

### How It Works

1. `stream.Watch()` adds `data-watch`, `data-signals`, and `data-effect` attributes
2. A MutationObserver-based JS worker collects all watch values and opens a single SSE connection to the stream endpoint
3. When the server publishes via `bus.NotifyCreated(...)`, the relay pushes a per-domain signal (e.g., `_ds_customers`)
4. Datastar evaluates the `data-effect` expression — if the action matches, it triggers `@get()` to reload the component
5. Each domain gets its own signal — no O(N) re-evaluation

---

## 8. Complete CRUD Example

### Handler

```go
type customerHandlers struct {
    bus       *pubsub.Bus
    mu        sync.RWMutex
    customers []Customer
    nextID    int
}

func (h *customerHandlers) register(r chi.Router) {
    r.Get("/customers/list", h.list())
    r.Get("/customers/count", h.count())
    r.Get("/customers/new", h.newDrawer())
    r.Post("/customers/create", h.create())
}

func (h *customerHandlers) newDrawer() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        wxctx := dsx.FromContext(r.Context())
        sse := datastar.NewSSE(w, r)
        _ = ds.Send.Drawer(r.Context(), sse,
            pages.CustomerDrawer(wxctx.APIPath("/customers/create")))
    }
}

func (h *customerHandlers) create() http.HandlerFunc {
    return form.Handler(
        newCustomerSignals{},
        func(formID string, r *http.Request) []form.FieldError {
            var signals newCustomerSignals
            if err := ds.ReadSignals(formID, r, &signals); err != nil {
                return []form.FieldError{{Field: "error", Message: "Failed to read form data"}}
            }

            var errs []form.FieldError
            if signals.Name == "" {
                errs = append(errs, form.FieldError{Field: "name_error", Message: "Name is required"})
            }
            if signals.Email == "" {
                errs = append(errs, form.FieldError{Field: "email_error", Message: "Email is required"})
            }
            if len(errs) > 0 {
                return errs
            }

            // Save
            h.mu.Lock()
            id := h.nextID
            h.nextID++
            h.customers = append(h.customers, Customer{
                ID: id, Name: signals.Name, Email: signals.Email,
            })
            h.mu.Unlock()

            // Publish — all watchers auto-reload
            _ = h.bus.NotifyCreated(r.Context(), "customers", strconv.Itoa(id))
            return nil
        },
        func(formID string, sse *datastar.ServerSentEventGenerator) {
            _ = ds.Send.HideDrawer(sse)
            _ = ds.Send.Toast(sse, ds.ToastSuccess, "Customer added")
        },
    )
}

func (h *customerHandlers) list() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        h.mu.RLock()
        rows := make([]pages.CustomerRow, len(h.customers))
        for i, c := range h.customers {
            rows[i] = pages.CustomerRow{Name: c.Name, Email: c.Email}
        }
        h.mu.RUnlock()
        sse := datastar.NewSSE(w, r)
        _ = ds.Send.Patch(sse, pages.CustomerTableBody(rows))
    }
}
```

### Page Templ

```go
templ Customers() {
    {{ wxctx := dsx.FromContext(ctx) }}

    // Count — watches Any change
    <div { stream.Watch(ctx, "customers",
        stream.Any.Get(wxctx.APIPath("/customers/count")))... }>
        <div id="customer-count"
            { ds.Init(ds.GetOnce(wxctx.APIPath("/customers/count")))... }>—</div>
    </div>

    // Table — watches Structural changes only
    <div { stream.Watch(ctx, "customers",
        stream.Structural.Get(wxctx.APIPath("/customers/list")))... }>
        <table class="table">
            <tbody id="customer-table-body"
                { ds.Init(ds.GetOnce(wxctx.APIPath("/customers/list")))... }>
                Loading...
            </tbody>
        </table>
    </div>

    // Add button — opens drawer
    @button.Button(button.Props{
        Variant:    button.VariantPrimary,
        Attributes: ds.OnClick(ds.GetOnce(wxctx.APIPath("/customers/new"))),
    }) {
        Add Customer
    }
}
```

### Drawer Form Templ

```go
templ CustomerDrawer(actionURL string) {
    <h2 class="text-xl font-bold mb-6">Add Customer</h2>
    @form.Form(form.Props{
        ID:      "new-customer",
        Action:  actionURL,
        Signals: newCustomerSignals{},
    }) {
        @form.Field() {
            @form.Label() { Name }
            <input type="text" class="input w-full"
                { ds.Bind("new-customer", "name")... } />
            @form.Error("$new_customer.name_error")
        }
        @form.Field() {
            @form.Label() { Email }
            <input type="email" class="input w-full"
                { ds.Bind("new-customer", "email")... } />
            @form.Error("$new_customer.email_error")
        }
        @form.Submit(form.SubmitProps{
            FormID:  "new-customer",
            Variant: button.VariantPrimary,
            Class:   "w-full",
        }) {
            Save Customer
        }
    }
}
```

---

## 9. Context and API Paths

Always use `dsx.FromContext(ctx)` to get the request context, and `wxctx.APIPath()` for handler URLs:

```go
// In templ
{{ wxctx := dsx.FromContext(ctx) }}
{ ds.OnClick(ds.GetOnce(wxctx.APIPath("/my-handler")))... }

// In handler
wxctx := dsx.FromContext(r.Context())
_ = ds.Send.Drawer(r.Context(), sse, myPage(wxctx.APIPath("/submit")))
```

**Never hardcode paths** — `APIPath` prepends the `BasePath` so routes work at any mount point.

---

## 10. Middleware Setup

```go
r := chi.NewRouter()
r.Use(dsx.Middleware(dsx.MiddlewareConfig{
    Secret: []byte("minimum-32-bytes-for-hmac-key!!!!!"),
    Secure: true,  // true for HTTPS/production
}))
r.Use(dsx.SecurityHeadersMiddleware(true))  // true adds HSTS

r.Handle("/assets/*", http.FileServer(...))

// Pattern resolver — maps watch domains to pub/sub subscription patterns
resolver := func(_ context.Context, watch string) string {
    domain, entityID, hasID := strings.Cut(watch, ".")
    if !hasID || entityID == "" {
        return fmt.Sprintf("%s.%s.change.%s.>", tenantID, workspaceID, domain)
    }
    return fmt.Sprintf("%s.%s.change.%s.%s.>", tenantID, workspaceID, domain, entityID)
}
relay := stream.New(ps, resolver)
r.Get("/stream", relay.Handler())  // SSE stream endpoint
```

The middleware:
- Creates/reads session cookie (`dsx_session`)
- Creates/reads signed CSRF cookie (`dsx_csrf`) using HMAC-SHA256
- Validates CSRF on POST/PUT/PATCH/DELETE via `X-CSRF-Token` header
- Reads theme cookie (`dsx_theme`)
- All `ds.Post/Put/Patch/Delete` helpers automatically include the CSRF header

---

## 11. Available UI Components

70+ components in `ui/`. Key interactive ones:

| Component | Import | Key Features |
|-----------|--------|--------------|
| `form` | `ui/form` | SSE forms with validation, error/success signals |
| `button` | `ui/button` | Variants, sizes, loading state |
| `modal` | `ui/modal` | Signal-driven show/hide |
| `drawer` | `ui/drawer` | Responsive sidebar |
| `dropdown` | `ui/dropdown` | Click/hover, outside-click close |
| `accordion` | `ui/accordion` | Single/multi expand |
| `tab` | `ui/tab` | Tab panels |
| `combobox` | `ui/combobox` | Searchable single-select (server-driven) |
| `multiselect` | `ui/multiselect` | Searchable multi-select (server-driven) |
| `calendar` | `ui/calendar` | Single/range date selection |
| `fileupload` | `ui/fileupload` | Multi-file upload with progress |
| `toast` | `ui/toast` | Container for SSE-driven toasts |
| `aichat` | `ui/aichat` | Chat interface (widget + fullpage) |
| `commandbar` | `ui/commandbar` | Multi-mode command input |
| `themecontroller` | `ui/themecontroller` | Theme switching with persistence |
| `validator` | `ui/validator` | Debounced inline input validation |

Display components: `card`, `badge`, `alert`, `stat`, `table`, `list`, `avatar`, `progress`, `skeleton`, `tooltip`, `chat`, `timeline`, `navbar`, `menu`, `breadcrumbs`, `footer`, `separator`, and more.

---

## 12. Signal Naming Convention

- Component IDs with hyphens are sanitized: `"my-form"` → signal namespace `my_form`
- Signal references use `$`: `$my_form.email`, `$my_form.email_error`
- Error signals follow pattern: `fieldname_error` (auto-derived from json tags)
- Form-level errors use the special field name `"error"` → triggers toast

---

## 13. Best Practices

### DO

- **Use `ds.*` helpers for ALL Datastar attributes** — never write raw `data-on-click` (hyphen is silently ignored)
- **Use `wxctx.APIPath()`** for all handler URLs
- **Use DaisyUI theme tokens** (`text-base-content`, `bg-base-100`, `btn-primary`) — never hardcode colors
- **Validate server-side only** — forms set `novalidate`
- **Read signals before creating SSE**: `ds.ReadSignals()` then `datastar.NewSSE()`
- **Use `form.Handler()`** for form submissions — it handles error clearing, signal patching, and toast errors
- **Use `stream.Watch()`** for live updates — pick the narrowest action set (`Structural` > `Any`)
- **Use `utils.TwMerge()`** for all class composition
- **Wrap errors**: `fmt.Errorf("context: %w", err)`
- **Protect shared state with mutexes** in handlers
- **Publish via bus** after mutations for cross-tab reactivity

### DON'T

- **Don't write custom CSS or JavaScript** — DaisyUI + Datastar only
- **Don't use browser form validation** — all validation is server-side
- **Don't use `data-on-click`** (hyphen) — use `ds.OnClick()` (colon)
- **Don't hardcode colors** — use theme tokens
- **Don't create SSE before reading signals** — body is consumed once
- **Don't use `stream.Any` when `stream.Structural` suffices** — reduces unnecessary reloads
- **Don't skip CSRF** — `ds.Post/Put/Patch/Delete` include it automatically
- **Don't return HTML from handlers** — always use SSE (`datastar.NewSSE()`)

---

## 14. Event Modifiers in Datastar

Datastar uses double underscore `__` for event modifiers in `ds.On()`:

```go
ds.On("submit__prevent", expr)                  // preventDefault
ds.On("click__once", expr)                      // fire once
ds.On("input__debounce_300", expr)              // debounce 300ms
ds.On("keydown__key_enter", expr)               // specific key
ds.On("click__outside", expr)                   // click outside element
ds.On("scroll__throttle_100", expr)             // throttle 100ms
```

---

## 15. File Uploads

```go
// Templ
@fileupload.FileUpload(fileupload.Props{
    ID:        "my-upload",
    Multiple:  true,
    Accept:    "image/*",
    UploadURL: wxctx.APIPath("/upload/files"),
    RemoveURL: wxctx.APIPath("/upload/remove"),
})

// Handler
store := fileupload.NewStore()
r.Post("/upload/files", fileupload.UploadHandler(store,
    fileupload.WithAllowedTypes("image/"),
    fileupload.WithMaxFiles(3),
))
r.Post("/upload/remove", fileupload.RemoveHandler(store))
```

---

## 16. After Writing Code

Always run these commands after editing `.templ` files:

```sh
go tool templ fmt ./path/to/file.templ
go tool templ generate
go build ./cmd/...
```

If new DaisyUI classes were introduced:

```sh
go tool gotailwind -i static/css/input.css -o static/css/output.css
```

---
---

# Appendix A — Datastar + Go Backend: Comprehensive Reference

> **Version**: Datastar v1.0.0-RC.7 | Go SDK v1.0.3 (`github.com/starfederation/datastar-go`)

## A.1 Architecture Overview

Datastar is a **hypermedia-driven reactive framework** (~11 KiB) that combines backend-driven UI (like htmx) with frontend reactivity (like Alpine.js) in a single library. The core principles are:

- **Backend drives the frontend**: The server sends HTML fragments and state updates over Server-Sent Events (SSE). There is no separate REST API layer.
- **No JavaScript build step**: All frontend reactivity is declared via `data-*` HTML attributes.
- **Signals are the state model**: Reactive state lives in "signals" (prefixed with `$` in expressions). The backend can read, modify, and patch signals.
- **DOM morphing**: Datastar uses Idiomorph to intelligently morph only changed parts of the DOM, preserving state and event listeners.

### Request/Response Flow

```
Browser                          Go Backend
  |                                  |
  | -- @get('/endpoint') ----------> |  (sends all signals as query param or JSON body)
  |                                  |
  | <-- text/event-stream ---------- |  (streams SSE events: patch-elements, patch-signals)
  |                                  |
  |  [Datastar morphs DOM &          |
  |   updates signals reactively]    |
```

### Content Types the Backend Can Return

| Content-Type | Behavior |
|---|---|
| `text/event-stream` | Standard SSE with Datastar events (recommended) |
| `text/html` | HTML elements patched into DOM by ID |
| `application/json` | JSON signals patched into state |
| `text/javascript` | JavaScript executed in browser |

## A.2 Installation & Setup

### Frontend (HTML)

```html
<script type="module" src="https://cdn.jsdelivr.net/gh/starfederation/[email protected]/bundles/datastar.js"></script>
```

Or self-host:

```html
<script type="module" src="/static/js/datastar.js"></script>
```

### Backend (Go)

```bash
go get github.com/starfederation/datastar-go
```

```go
import "github.com/starfederation/datastar-go/datastar"
```

## A.3 Frontend: Datastar Attributes Reference

### Signal & State Attributes

#### `data-signals`

Patches (adds, updates, removes) one or more signals.

```html
<div data-signals:count="0"></div>
<div data-signals="{name: 'Alice', age: 30, active: true}"></div>
<div data-signals:user.name="'Bob'"></div>
<div data-signals="{user: {name: 'Bob', email: 'bob@example.com'}}"></div>
<div data-signals="{obsoleteSignal: null}"></div>
```

**Modifiers:**
- `__ifmissing` — only set if signal doesn't already exist: `data-signals:foo__ifmissing="1"`

**Rules:**
- Signals beginning with `_` are NOT sent to the backend by default.
- Signal names cannot contain `__` (reserved for modifier delimiter).
- Keys in `data-signals:*` are auto-converted to camelCase.

#### `data-bind`

Two-way data binding between a signal and an input/select/textarea element.

```html
<input data-bind:name />
<input data-bind:username value="defaultUser" />
<div data-signals:count="0">
    <select data-bind:count>
        <option value="10">10</option>
    </select>
</div>
<div data-signals:colors="[]">
    <input data-bind:colors type="checkbox" value="red" />
    <input data-bind:colors type="checkbox" value="blue" />
</div>
```

#### `data-computed`

Creates a read-only derived signal.

```html
<div data-computed:full-name="$firstName + ' ' + $lastName"></div>
<div data-computed:total="$price * $quantity"></div>
```

#### `data-ref`

Creates a signal that references a DOM element.

```html
<canvas data-ref:my-canvas></canvas>
<div data-text="$myCanvas.tagName"></div>
```

### Display & Rendering Attributes

```html
<!-- data-text: Binds element text content -->
<span data-text="$count"></span>
<span data-text="`Hello, ${$name}!`"></span>

<!-- data-show: Shows/hides based on expression -->
<div data-show="$isLoggedIn">Welcome back!</div>

<!-- data-class: Conditionally adds/removes CSS classes -->
<div data-class:font-bold="$isImportant"></div>
<div data-class="{active: $isActive, 'text-red-500': $hasError}"></div>

<!-- data-attr: Sets any HTML attribute reactively -->
<button data-attr:disabled="$isSubmitting"></button>
<a data-attr:href="`/users/${$userId}`"></a>

<!-- data-style: Sets inline CSS styles reactively -->
<div data-style:color="$isError ? 'red' : 'green'"></div>
```

### Event Attributes

#### `data-on`

```html
<button data-on:click="@post('/submit')">Submit</button>
<button data-on:click="$count++">Increment</button>
<form data-on:submit="@post('/login')">...</form>
```

**Modifiers (chainable with `__`):**

| Modifier | Description |
|---|---|
| `__once` | Fire only once |
| `__debounce.500ms` | Debounce |
| `__throttle.1s` | Throttle |
| `__delay.500ms` | Delay execution |
| `__window` | Listen on window |
| `__outside` | Trigger when event is outside element |
| `__prevent` | Call `preventDefault()` |
| `__stop` | Call `stopPropagation()` |
| `__passive` | Passive event listener |
| `__capture` | Capture phase |

```html
<button data-on:click__debounce.300ms="@post('/search')">Search</button>
<div data-on:click__outside="$menuOpen = false"></div>
```

#### `data-on-intersect`

```html
<div data-on-intersect="@get('/load-more')">Loading...</div>
<div data-on-intersect__once__full="$seen = true"></div>
```

#### `data-on-interval`

```html
<div data-on-interval="@get('/poll')"></div>
<div data-on-interval__duration.5s="@get('/check-status')"></div>
```

### Lifecycle & Control Attributes

```html
<!-- data-init: Runs expression on mount -->
<div data-init="@get('/initial-data')"></div>

<!-- data-effect: Runs on load AND when referenced signals change -->
<div data-effect="document.title = `Count: ${$count}`"></div>

<!-- data-indicator: Boolean signal true while fetch in-flight -->
<button data-on:click="@get('/data')" data-indicator:loading>Load</button>
<div data-show="$loading">Loading...</div>

<!-- data-ignore: Skip Datastar processing -->
<div data-ignore>...</div>

<!-- data-ignore-morph: Skip morphing during PatchElements -->
<div data-ignore-morph><video>...</video></div>

<!-- data-preserve-attr: Preserve attributes during morphing -->
<details open data-preserve-attr="open">...</details>
```

### Debugging

```html
<pre data-json-signals></pre>
<pre data-json-signals="{include: /user/}"></pre>
```

## A.4 Frontend: Actions Reference

### Utility Actions

```html
<!-- @peek: Access signal value without subscribing -->
<div data-text="$foo + @peek(() => $bar)"></div>

<!-- @setAll: Set multiple signals at once -->
<button data-on:click="@setAll(false, {include: /^is/})">Reset All</button>

<!-- @toggleAll: Toggle boolean signals -->
<button data-on:click="@toggleAll({include: /^settings\./})">Toggle Settings</button>
```

### Backend Actions

All backend actions send an HTTP request. By default, ALL signals (except `_`-prefixed) are included.

```html
<button data-on:click="@get('/api/data')">Load</button>
<button data-on:click="@post('/api/submit')">Submit</button>
<button data-on:click="@put('/api/users/1')">Update</button>
<button data-on:click="@delete('/api/users/1')">Delete</button>
```

#### Backend Action Options

| Option | Type | Default | Description |
|---|---|---|---|
| `contentType` | `'json'` / `'form'` | `'json'` | `'form'` sends as form data |
| `filterSignals` | `{include: RegExp}` | all non-`_` signals | Filter which signals to send |
| `headers` | `object` | `{}` | Additional request headers |
| `openWhenHidden` | `boolean` | `false`/`true` | Keep SSE open when tab hidden |
| `retry` | string | `'auto'` | Retry strategy |
| `retryMaxCount` | `number` | `10` | Max retry attempts |
| `requestCancellation` | string | `'auto'` | `'disabled'` for persistent streams |

## A.5 Frontend: Datastar Expressions

Signals are accessed with `$` prefix:

```html
<div data-text="$count"></div>
<div data-text="$user.name"></div>
<div data-text="$name.toUpperCase()"></div>
<div data-text="`Hello, ${$name}!`"></div>
<div data-text="$count > 0 ? 'Has items' : 'Empty'"></div>
```

**Available Variables:** `$signalName`, `el` (DOM element), `evt` (event object in `data-on`).

**Casing Rules:**
- Signal-defining attributes (bind, signals, computed, ref, indicator): hyphen -> camelCase
- Other attributes (class, on, attr, style): hyphen -> kebab-case (default)

## A.6 Backend: Go SDK API Reference

### Reading Signals

```go
store := &struct {
    Count int    `json:"count"`
    Name  string `json:"name"`
}{}
if err := datastar.ReadSignals(r, store); err != nil {
    http.Error(w, err.Error(), http.StatusBadRequest)
    return
}
```

### Creating an SSE Writer

```go
sse := datastar.NewSSE(w, r)
// With compression:
sse := datastar.NewSSE(w, r, datastar.WithCompression(datastar.WithGzip(), datastar.WithBrotli()))
```

### Patching Elements (DOM Updates)

```go
sse.PatchElements(`<div id="output">Hello World!</div>`)
sse.PatchElementf(`<div id="user-%d">%s</div>`, userID, userName)
sse.PatchElements(`<li>New Item</li>`, datastar.WithSelector("#item-list"), datastar.WithModeAppend())
sse.PatchElements(`<p>New content</p>`, datastar.WithSelector("#container"), datastar.WithModeInner())
```

**PatchElement Options:**

| Option | Description |
|---|---|
| `WithModeOuter()` | Morphs outer HTML (default) |
| `WithModeInner()` | Morphs inner HTML |
| `WithModeReplace()` | Replaces outer HTML (no morph) |
| `WithModePrepend()` | Prepend to target children |
| `WithModeAppend()` | Append to target children |
| `WithModeBefore()` | Insert before target |
| `WithModeAfter()` | Insert after target |
| `WithModeRemove()` | Remove target |
| `WithSelector("#id")` | Target CSS selector |
| `WithViewTransitions()` | Enable view transitions |

### Removing Elements

```go
sse.RemoveElement("#temporary-message")
sse.RemoveElementByID("notification-5")
```

### Patching Signals (State Updates)

```go
_ = sse.MarshalAndPatchSignals(map[string]any{
    "count":   42,
    "message": "Updated!",
    "user": map[string]any{"name": "Alice", "email": "alice@example.com"},
})
sse.PatchSignals([]byte(`{"count": 42}`))
_ = sse.MarshalAndPatchSignalsIfMissing(map[string]any{"theme": "dark"})
```

### Executing JavaScript

```go
sse.ExecuteScript(`console.log("Hello from server!")`)
```

### Redirecting

```go
sse.Redirect("/dashboard")
sse.Redirectf("/users/%d/profile", userID)
```

### Connection State

```go
sse.IsClosed()
sse.Context()
```

### Convenience Functions for Templates

```go
datastar.GetSSE("/endpoint")      // returns: @get('/endpoint')
datastar.PostSSE("/endpoint")     // returns: @post('/endpoint')
datastar.PutSSE("/endpoint")
datastar.PatchSSE("/endpoint")
datastar.DeleteSSE("/endpoint")
```

## A.7 SSE Events Protocol

### `datastar-patch-elements`

```
event: datastar-patch-elements
data: elements <div id="foo">Hello world!</div>

```

Multi-line / options:

```
event: datastar-patch-elements
data: selector #target
data: mode inner
data: useViewTransition true
data: elements <div>
data: elements     Hello world!
data: elements </div>

```

### `datastar-patch-signals`

```
event: datastar-patch-signals
data: signals {count: 42, name: "Alice"}

```

Only-if-missing:

```
event: datastar-patch-signals
data: onlyIfMissing true
data: signals {theme: "dark", lang: "en"}

```

## A.8 Complete Patterns

### Real-Time Updates (Long-Lived SSE)

```go
func clockHandler(w http.ResponseWriter, r *http.Request) {
    sse := datastar.NewSSE(w, r)
    ticker := time.NewTicker(1 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-r.Context().Done():
            return
        case t := <-ticker.C:
            if sse.IsClosed() { return }
            _ = sse.PatchElements(fmt.Sprintf(`<div id="clock">%s</div>`, t.Format("15:04:05")))
        }
    }
}
```

```html
<div id="clock" data-init="@get('/clock')">--:--:--</div>
```

### Loading Indicators

```html
<div data-signals="{query: ''}">
    <input data-bind:query placeholder="Search..." />
    <button data-on:click="@post('/search')" data-indicator:searching data-attr:disabled="$searching">
        <span data-show="!$searching">Search</span>
        <span data-show="$searching">Searching...</span>
    </button>
    <div id="results"></div>
</div>
```

### Debounced Search

```html
<input data-bind:query data-on:input__debounce.300ms="@get('/search')" placeholder="Search..." />
<div id="results"></div>
```

### Infinite Scroll / Load More

```html
<div id="feed"><!-- items --></div>
<div data-signals:page="1" data-on-intersect__once="$page++; @get('/feed')" id="load-trigger">
    Loading more...
</div>
```

### Multiple Elements in One Response

```go
sse := datastar.NewSSE(w, r)
_ = sse.PatchElements(`<div id="header">Updated Header</div>`)
_ = sse.PatchElements(`<div id="sidebar">Updated Sidebar</div>`)
_ = sse.MarshalAndPatchSignals(map[string]any{"lastUpdated": time.Now().Format(time.RFC3339)})
```

## A.9 Security Guidelines

- **Always validate signals server-side** — signal values can be modified by users
- **Escape HTML** when injecting user content: `html.EscapeString(userInput)`
- **Use `data-ignore`** for untrusted user-generated content
- **Signals starting with `_`** are local-only (not sent to backend)

## A.10 Quick Reference Card

### Frontend Cheatsheet

```
SIGNALS:        data-signals:name="'value'"     data-signals="{a: 1, b: 2}"
BIND:           data-bind:name                  (two-way binding to input)
COMPUTED:       data-computed:full="$a + $b"    (read-only derived signal)
TEXT:           data-text="$name"               (bind text content)
SHOW:           data-show="$visible"            (toggle visibility)
CLASS:          data-class:active="$isActive"   (toggle CSS class)
ATTR:           data-attr:disabled="$loading"   (set any HTML attribute)
STYLE:          data-style:color="$color"       (set inline style)
ON EVENT:       data-on:click="@post('/url')"   (event listener)
INIT:           data-init="@get('/data')"       (run on load)
EFFECT:         data-effect="$c = $a + $b"      (reactive side effect)
INDICATOR:      data-indicator:loading           (true during fetch)
REF:            data-ref:el                     (DOM element reference)
INTERVAL:       data-on-interval="@get('/poll')" (periodic execution)
INTERSECT:      data-on-intersect="@get('/more')"(viewport intersection)
DEBUG:          data-json-signals                (show all signals as JSON)
```

### Go Backend Cheatsheet

```go
// Read signals
store := &MyStore{}
datastar.ReadSignals(r, store)

// Create SSE writer
sse := datastar.NewSSE(w, r)

// Patch DOM elements
sse.PatchElements(`<div id="x">content</div>`)
sse.PatchElements(html, datastar.WithSelector("#target"), datastar.WithModeAppend())

// Remove elements
sse.RemoveElement("#selector")
sse.RemoveElementByID("element-id")

// Patch signals
_ = sse.MarshalAndPatchSignals(map[string]any{"key": "value"})

// Execute JS / Redirect / Check connection
sse.ExecuteScript(`console.log("hello")`)
sse.Redirect("/new-url")
sse.IsClosed()
```

---
---

# Appendix B — HTML Elements & Datastar Interaction Reference

> Every HTML element, what events it fires, and how to wire it up with Datastar.

## B.1 How Datastar Binds to HTML Elements

| Datastar Attribute | What It Does | Applies To |
|---|---|---|
| `data-signals` | Declares reactive state | Any element (typically container) |
| `data-bind:ATTR` | Two-way binds signal to form element value | `<input>`, `<select>`, `<textarea>` only |
| `data-on:EVENT` | Listens for DOM event, runs expression | Any element |
| `data-text` | Sets textContent reactively | Any element |
| `data-attr:ATTR` | Sets HTML attribute reactively | Any element |
| `data-class:NAME` | Toggles CSS class reactively | Any element |
| `data-style:PROP` | Sets inline style reactively | Any element |
| `data-show` | Toggles display based on signal | Any element |
| `data-init` | Runs expression on mount | Any element |
| `data-effect` | Reactive side-effect when dependencies change | Any element |
| `data-indicator:NAME` | Auto-true while SSE request in-flight | Any element |

**Critical rule**: `data-bind` only works on form elements (`<input>`, `<select>`, `<textarea>`). For everything else, use `data-text`, `data-attr`, `data-class`, `data-style`, or `data-show`.

## B.2 Form Input Elements

### `<input type="text">` (and `search`, `url`, `tel`, `email`, `password`)

Key events: `input` (every keystroke), `change` (on blur), `focus`, `blur`, `keydown`, `keyup`, `paste`.

```html
<input type="text" data-bind:query />
<input type="text" data-bind:q data-on:input__debounce.250ms="@get('/search')" />
<input type="text" data-bind:input data-on:keydown="if(evt.key==='Enter') @post('/submit')" />
<input type="text" data-attr:disabled="$loading" />
```

### `<input type="number">`

Same events as text. Value is still a string — use `+$quantity` or `Number($quantity)` to coerce.

```html
<input type="number" data-bind:quantity min="0" max="100" step="1" />
```

### `<input type="range">`

Events: `input` (continuously while sliding), `change` (on release).

```html
<input type="range" data-bind:volume min="0" max="100" />
<span data-text="$volume"></span>
```

### `<input type="checkbox">`

`data-bind` binds to the **checked state** (boolean), not the value.

```html
<input type="checkbox" data-bind:darkMode />
<input type="checkbox" data-bind:enabled data-on:change="@post('/toggle')" />
```

### `<input type="radio">`

All radios in a group bind to the **same signal name**. Value comes from the `value` attribute.

```html
<label><input type="radio" name="size" value="sm" data-bind:size /> Small</label>
<label><input type="radio" name="size" value="md" data-bind:size /> Medium</label>
<label><input type="radio" name="size" value="lg" data-bind:size /> Large</label>
```

### `<input type="file">`

`data-bind` does NOT work with file inputs. Use `data-on:change`.

```html
<input type="file" data-on:change="@post('/upload')" accept="image/*" multiple />
```

### `<input type="date">`, `<input type="time">`, `<input type="datetime-local">`

```html
<input type="date" data-bind:startDate />
<input type="date" data-bind:endDate data-attr:min="$startDate" />
```

### `<input type="color">`

```html
<input type="color" data-bind:themeColor />
<div data-style:background-color="$themeColor">Preview</div>
```

## B.3 Button Elements

Default type is `"submit"` inside a form, `"button"` outside — always specify `type` explicitly.

Events: `click`, `mousedown`/`mouseup`, `focus`/`blur`, `keydown`.

```html
<button type="button" data-on:click="@post('/api/items')">Add Item</button>

<!-- With loading indicator -->
<button type="button" data-on:click="@post('/api/save')" data-indicator:saving data-attr:disabled="$saving">
    <span data-show="!$saving">Save</span>
    <span data-show="$saving">Saving...</span>
</button>

<!-- Toggle signal -->
<button type="button" data-on:click="$showMenu = !$showMenu">Toggle Menu</button>
```

## B.4 Select & Option Elements

Events: `change`, `input`, `focus`/`blur`.

```html
<select data-bind:color>
    <option value="">-- Choose --</option>
    <option value="red">Red</option>
    <option value="green">Green</option>
</select>

<select data-bind:category data-on:change="@get('/products')">
    <option value="all">All</option>
    <option value="electronics">Electronics</option>
</select>

<div data-show="$category === 'other'">
    <input type="text" data-bind:otherCategory placeholder="Specify..." />
</div>
```

## B.5 Textarea

Events: Same as text input — `input`, `change`, `focus`, `blur`, `keydown`.

```html
<textarea data-bind:bio rows="5"></textarea>
<textarea data-bind:notes data-on:input__debounce.1000ms="@put('/api/notes')"></textarea>
```

## B.6 Form Element

With Datastar, you almost never use native form submission — use `data-on:submit__prevent`.

```html
<form data-on:submit__prevent="@post('/api/users')">
    <input type="text" data-bind:name required />
    <button type="submit">Submit</button>
</form>
```

**Critical**: Use `__prevent` modifier to prevent native form submission (page navigation).

## B.7 Container Elements

`<div>`, `<section>`, `<article>`, `<main>`, `<aside>`, `<header>`, `<footer>`, `<nav>` — all work identically for Datastar.

```html
<div data-signals="{count: 0, name: 'World'}">
    <span data-text="$name"></span>
</div>
<div id="container" data-init="@get('/api/data')"></div>
<div data-show="$showPanel">Panel content...</div>
<div data-on-intersect="@get('/api/more-items')">Loading more...</div>
<div data-on-interval__duration.5s="@get('/api/status')">
    Status: <span data-text="$status"></span>
</div>
```

## B.8 Text Content Elements

`<span>`, `<p>`, `<h1>`-`<h6>`, `<label>`, `<strong>`, `<em>`, etc. — not interactive, targets for `data-text`, `data-show`, `data-class`.

```html
<span data-text="$username"></span>
<span data-text="'$' + Number($price).toFixed(2)"></span>
<span data-show="$error" class="text-red">Error: <span data-text="$error"></span></span>
```

## B.9 Table Elements

```html
<th data-on:click="$sortBy = 'name'; @get('/api/users')" style="cursor: pointer;">Name</th>
<tr data-on:click="@get('/api/users/1')" data-class:bg-primary="$selectedId === 1">
    <td>John</td>
</tr>
```

## B.10 Link & Navigation

```html
<a href="/about">About</a>
<a href="#" data-on:click__prevent="@get('/api/panel/info')">Load Info</a>
<a data-attr:href="'/users/' + $userId">View Profile</a>
```

**When to use `<a>` vs `<button>`**: `<a>` for navigation (has `href`), `<button>` for actions (no `href`).

## B.11 Dialog & Disclosure

### `<dialog>`

```html
<dialog data-ref:myDialog
        data-effect="$showDialog ? $myDialog.showModal() : $myDialog.close()"
        data-on:close="$showDialog = false">
    <button data-on:click="$showDialog = false">Cancel</button>
    <button data-on:click="@delete('/api/items/1'); $showDialog = false">Delete</button>
</dialog>
<button data-on:click="$showDialog = true">Open Dialog</button>
```

### `<details>` / `<summary>`

```html
<details data-on:toggle="if(evt.target.open) @get('/api/section/details')">
    <summary>Show more info</summary>
    <div id="details-content">...</div>
</details>
```

## B.12 Progress & Meter

```html
<progress data-attr:value="$uploadProgress" max="100"></progress>
<progress data-show="$isLoading"></progress>
<meter data-attr:value="$diskUsage" min="0" max="100" low="50" high="80" optimum="30"></meter>
```

## B.13 Fieldset & Legend

Disabling a `<fieldset>` disables ALL descendant form controls.

```html
<fieldset data-attr:disabled="$isSubmitting">
    <legend>Shipping Address</legend>
    <input type="text" data-bind:street placeholder="Street" />
    <input type="text" data-bind:city placeholder="City" />
</fieldset>
```

## B.14 Global Events Reference

### Mouse Events

| Event | When It Fires |
|---|---|
| `click` | Left-click (or Enter/Space on focused interactive element) |
| `dblclick` | Double-click |
| `contextmenu` | Right-click |
| `mouseenter` / `mouseleave` | Mouse enters/exits (no bubbling) |
| `mouseover` / `mouseout` | Mouse enters/exits (bubbles) |

### Keyboard Events

| Event | When It Fires |
|---|---|
| `keydown` | Key pressed (repeats if held). `evt.key` = character |
| `keyup` | Key released |

Common `evt.key` values: `'Enter'`, `'Escape'`, `'Tab'`, `'Backspace'`, `'ArrowUp'`, `'ArrowDown'`.

Modifiers: `evt.ctrlKey`, `evt.shiftKey`, `evt.altKey`, `evt.metaKey`.

### Focus Events

| Event | When It Fires |
|---|---|
| `focus` / `blur` | Element receives/loses focus (no bubbling) |
| `focusin` / `focusout` | Same but bubbles |

### Drag & Drop Events

| Event | Target | Notes |
|---|---|---|
| `dragstart` / `drag` / `dragend` | Dragged element | |
| `dragenter` / `dragover` / `dragleave` / `drop` | Drop target | Must `__prevent` on dragover and drop |

### Touch Events (Mobile)

`touchstart`, `touchmove`, `touchend`, `touchcancel`

### Scroll Events

`scroll` (high frequency — always throttle), `scrollend`

## B.15 Datastar Attribute Quick Reference

### Event Modifiers

| Modifier | Effect |
|---|---|
| `__prevent` | Calls `evt.preventDefault()` |
| `__stop` | Calls `evt.stopPropagation()` |
| `__once` | Handler fires only once |
| `__outside` | Fires when click is OUTSIDE this element |
| `__debounce.Xms` | Debounce by X ms |
| `__throttle.Xms` | Throttle to at most once per X ms |
| `__self` | Only fires if target === this element |
| `__capture` | Capture phase |
| `__passive` | Passive listener |

Multiple modifiers combine: `data-on:click__prevent__stop`.

### Elements That Support `data-bind`

| Element | Binds To | Signal Type |
|---|---|---|
| `<input type="text">` (and text-like) | `.value` | `string` |
| `<input type="number">` | `.value` | `string` (coerce with `+$signal`) |
| `<input type="range">` | `.value` | `string` |
| `<input type="checkbox">` | `.checked` | `boolean` |
| `<input type="radio">` | `.value` of selected | `string` |
| `<input type="date/time">` | `.value` | `string` |
| `<input type="color">` | `.value` | `string` (`"#rrggbb"`) |
| `<select>` | `.value` of selected option | `string` |
| `<select multiple>` | Array of selected values | `array` |
| `<textarea>` | `.value` | `string` |

Everything else (`<div>`, `<span>`, `<button>`, etc.) — use `data-text`, `data-attr`, `data-class`, `data-style`, or `data-show`.

### Boolean Attributes (control with `data-attr`)

`disabled`, `checked`, `selected`, `required`, `readonly`, `multiple`, `autofocus`, `hidden`, `open`, `novalidate`, `inert`

```html
<button data-attr:disabled="$isSubmitting">Submit</button>
<div data-attr:inert="$modalOpen">Background content</div>
```

### The `id` Attribute and Datastar Targeting

**Every element that the Go backend needs to patch MUST have an `id`**. The SDK targets elements by ID:

```go
sse.PatchElements(`<div id="user-info">Updated content</div>`)
sse.PatchElements(`<span>New text</span>`, datastar.WithSelector("#user-name"))
sse.RemoveElementByID("notification-banner")
```

## B.16 Element Decision Tree

```
What do I need?
|
+-- User types text        -> <input type="text"> + data-bind:signal
+-- User types long text   -> <textarea> + data-bind:signal
+-- User picks from list   -> <select> + data-bind:signal
+-- User toggles on/off    -> <input type="checkbox"> + data-bind:signal
+-- User picks one of few  -> <input type="radio"> + data-bind:signal
+-- User picks number      -> <input type="number"> or <input type="range">
+-- User picks date/time   -> <input type="date/time/datetime-local">
+-- User uploads file      -> <input type="file"> + data-on:change
+-- User triggers action   -> <button type="button"> + data-on:click="@post(...)"
+-- User navigates         -> <a href="/path">
+-- User submits form      -> <button type="submit"> inside <form data-on:submit__prevent>
|
+-- Display reactive text  -> <span data-text="$signal">
+-- Show/hide content      -> <div data-show="$condition">
+-- Toggle CSS class       -> <element data-class:name="$condition">
+-- Set HTML attribute     -> <element data-attr:name="$expression">
|
+-- Load data on mount     -> <div data-init="@get('/api/data')">
+-- Stream real-time       -> <div data-init="@get('/api/stream', {requestCancellation:'disabled'})">
+-- Poll periodically      -> <div data-on-interval__duration.5s="@get('/api/status')">
+-- Load on scroll         -> <div data-on-intersect="@get('/api/more')">
|
+-- Modal dialog           -> <dialog> + data-ref + data-effect
+-- Disable form group     -> <fieldset data-attr:disabled="$submitting">
+-- Server updates DOM     -> Element has id -> sse.PatchElements(...)
```
