# Tutorial: Real-Time Customer List

Build a customer list where adding a new customer via a drawer form auto-updates the list in **all** open browser tabs.

## What we're building

1. A customer table that loads via SSE
2. An "Add Customer" button that opens a drawer form
3. Form validation with error feedback
4. On submit: drawer closes, toast appears, list auto-reloads everywhere

This tutorial uses `stream.Attrs` for reactive reloading, `ds.Send.Drawer` / `ds.Send.HideDrawer` / `ds.Send.Toast` for UI orchestration, and `form.Handler` for validation.

---

## 1. Setup — Relay and Bus

The stream package needs a pub/sub backend. For development, the in-process channel adapter works with no external dependencies:

```go
import (
    "github.com/laenen-partners/pubsub/chanpubsub"
    "github.com/laenen-partners/pubsub"
    "github.com/laenen-partners/dsx/stream"
)

ps := chanpubsub.New()
relay := stream.New(ps)
bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope(tenant, workspace))
```

For production with NATS:

```go
ps := natspubsub.New(nc)
relay := stream.New(ps)
bus := pubsub.NewBus(ps, "myapp", pubsub.WithScope(tenant, workspace))
```

---

## 2. Customer type + in-memory store

```go
type Customer struct {
    ID      int
    Name    string
    Email   string
    Company string
}

type customerHandlers struct {
    bus       *pubsub.Bus
    mu        sync.RWMutex
    customers []Customer
    nextID    int
}

func newCustomerHandlers(bus *pubsub.Bus) *customerHandlers {
    return &customerHandlers{
        bus: bus,
        customers: []Customer{
            {ID: 1, Name: "Alice Johnson", Email: "alice@example.com", Company: "Acme Corp"},
            {ID: 2, Name: "Bob Smith", Email: "bob@example.com", Company: "Globex Inc"},
        },
        nextID: 3,
    }
}
```

---

## 3. List handler — `ds.Send.Patch`

The list handler reads customers and patches the table body via SSE:

```go
func (h *customerHandlers) list() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        h.mu.RLock()
        rows := make([]pages.CustomerRow, len(h.customers))
        for i, c := range h.customers {
            rows[i] = pages.CustomerRow{Name: c.Name, Email: c.Email, Company: c.Company}
        }
        h.mu.RUnlock()

        sse := datastar.NewSSE(w, r)
        ds.Send.Patch(sse, pages.CustomerTableBody(rows))
    }
}
```

---

## 4. Page template — `stream.Attrs`

The table wrapper uses `stream.Attrs` to register the wildcard scope `customers:*`. When **any** `customers:{id}` scope is invalidated, the table auto-reloads:

```go
templ Customers() {
    {{ wxctx := dsx.FromContext(ctx) }}
    <div { stream.Attrs(ctx, "customers:*", wxctx.APIPath("/customers/list"))... }>
        <table class="table">
            <thead>
                <tr><th>Name</th><th>Email</th><th>Company</th></tr>
            </thead>
            <tbody
                id="customer-table-body"
                { ds.Init(ds.GetOnce(wxctx.APIPath("/customers/list")))... }
            >
                <tr><td colspan="3">Loading...</td></tr>
            </tbody>
        </table>
    </div>

    @button.Button(button.Props{
        Variant:    button.VariantPrimary,
        Attributes: ds.Merge(ds.OnClick(ds.GetOnce(wxctx.APIPath("/customers/new")))),
    }) {
        Add Customer
    }
}
```

Key points:
- `stream.Attrs` sets up both `data-signals` (stale flag) and `data-effect` (auto-reload)
- `ds.Init(ds.GetOnce(...))` loads the table body on first render
- The wildcard `customers:*` matches any `customers:{id}` invalidation

---

## 5. Drawer form

The drawer form uses `form.Form` for signal namespacing and `ds.Bind` for two-way binding:

```go
type newCustomerSignals struct {
    Name    string `json:"name"`
    Email   string `json:"email"`
    Company string `json:"company"`
}

templ CustomerDrawer() {
    {{ wxctx := dsx.FromContext(ctx) }}
    <h2 class="text-xl font-bold mb-6">Add Customer</h2>
    @form.Form(form.Props{
        ID:      "new-customer",
        Action:  wxctx.APIPath("/customers/create"),
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
        @form.Field() {
            @form.Label() { Company }
            <input type="text" class="input w-full"
                { ds.Bind("new-customer", "company")... } />
        }
        @form.Submit(form.SubmitProps{
            FormID: "new-customer", Variant: button.VariantPrimary,
        }) { Save Customer }
    }
}
```

---

## 6. Open drawer handler — `ds.Send.Drawer`

```go
func (h *customerHandlers) newDrawer() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        sse := datastar.NewSSE(w, r)
        ds.Send.Drawer(sse, pages.CustomerDrawer())
    }
}
```

---

## 7. Create handler — validate, save, invalidate

```go
func (h *customerHandlers) create() http.HandlerFunc {
    return form.Handler(
        // Signals struct — error fields derived automatically (name_error, email_error, company_error)
        newCustomerSignals{},
        // Validate
        func(formID string, r *http.Request) []form.FieldError {
            var signals newCustomerSignals
            if err := ds.ReadSignals(formID, r, &signals); err != nil {
                return []form.FieldError{{Field: "error", Message: "Failed to read form"}}
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
                ID: id, Name: signals.Name, Email: signals.Email, Company: signals.Company,
            })
            h.mu.Unlock()

            // Publish — triggers reload for any tab watching customers:*
            h.bus.NotifyCreated(r.Context(), "customers", strconv.Itoa(id))
            return nil
        },
        // On success
        func(formID string, sse *datastar.ServerSentEventGenerator) {
            ds.Send.HideDrawer(sse)
            ds.Send.Toast(sse, ds.ToastSuccess, "Customer added successfully")
        },
    )
}
```

---

## 8. Wire routes

```go
func (h *customerHandlers) register(r chi.Router) {
    r.Get("/customers/list", h.list())
    r.Get("/customers/new", h.newDrawer())
    r.Post("/customers/create", h.create())
}
```

---

## 9. Try it

1. Run the showcase: `go run ./cmd/showcase serve`
2. Open `/examples/customers` in two browser tabs
3. Click "Add Customer" in tab 1
4. Fill in the form and submit
5. Watch the list update in **both** tabs simultaneously

---

## Key takeaways

| Helper | What it does |
|--------|-------------|
| `stream.Attrs(ctx, scope, url)` | Registers a scope and returns `data-signals` + `data-effect` for auto-reload |
| `bus.NotifyCreated(ctx, "entity", "id")` | Publishes to a scope — all tabs watching a matching scope reload |
| `ds.Send.Drawer(sse, component)` | Renders a templ component inside a slide-in drawer via SSE |
| `ds.Send.HideDrawer(sse)` | Closes the drawer from the server side |
| `ds.Send.Toast(sse, level, msg)` | Appends a toast notification via SSE |
| `ds.Send.Patch(sse, component)` | Patches a templ component into the DOM via SSE |
| `form.Handler(signals, validate, onSuccess)` | Handles form submission with validation and SSE responses |
| Wildcard scopes | `customers:*` matches any `customers:{id}` invalidation |
