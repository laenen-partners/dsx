package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx"
	"github.com/laenen-partners/dsx/cmd/showcase/internal/pages"
	"github.com/laenen-partners/dsx/ds"
	"github.com/laenen-partners/dsx/stream"
	"github.com/laenen-partners/dsx/ui/form"
	"github.com/laenen-partners/dsx/utils/validators"
	"github.com/starfederation/datastar-go/datastar"
)

// Customer is a demo customer record.
type Customer struct {
	ID      int
	Name    string
	Email   string
	Company string
}

type customerHandlers struct {
	broker    *stream.Broker
	mu        sync.RWMutex
	customers []Customer
	nextID    int
}

func newCustomerHandlers(broker *stream.Broker) *customerHandlers {
	return &customerHandlers{
		broker: broker,
		customers: []Customer{
			{ID: 1, Name: "Alice Johnson", Email: "alice@example.com", Company: "Acme Corp"},
			{ID: 2, Name: "Bob Smith", Email: "bob@example.com", Company: "Globex Inc"},
			{ID: 3, Name: "Carol Williams", Email: "carol@example.com", Company: "Initech"},
		},
		nextID: 4,
	}
}

func (h *customerHandlers) register(r chi.Router) {
	r.Get("/customers/list", h.list())
	r.Get("/customers/count", h.count())
	r.Get("/customers/new", h.newDrawer())
	r.Post("/customers/create", h.create())
}

func (h *customerHandlers) list() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		customers := make([]pages.CustomerRow, len(h.customers))
		for i, c := range h.customers {
			customers[i] = pages.CustomerRow{
				Name:    c.Name,
				Email:   c.Email,
				Company: c.Company,
			}
		}
		h.mu.RUnlock()

		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Patch(sse, pages.CustomerTableBody(customers))
	}
}

func (h *customerHandlers) count() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		n := len(h.customers)
		h.mu.RUnlock()

		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Patch(sse, pages.CustomerCount(n))
	}
}

func (h *customerHandlers) newDrawer() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wxctx := dsx.FromContext(r.Context())
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Drawer(sse, pages.CustomerDrawer(wxctx.APIPath("/customers/create")))
	}
}

type newCustomerSignals struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Company string `json:"company"`
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
			} else {
				res := validators.Email(signals.Email, false)
				if !res.Valid {
					errs = append(errs, form.FieldError{Field: "email_error", Message: res.Error})
				}
			}
			if len(errs) > 0 {
				return errs
			}

			// Save customer.
			h.mu.Lock()
			id := h.nextID
			h.nextID++
			h.customers = append(h.customers, Customer{
				ID:      id,
				Name:    signals.Name,
				Email:   signals.Email,
				Company: signals.Company,
			})
			h.mu.Unlock()

			// Invalidate so all tabs watching customers:* reload.
			if err := h.broker.Invalidate("customers:" + strconv.Itoa(id)); err != nil {
				return []form.FieldError{{Field: "error", Message: fmt.Sprintf("Failed to publish invalidation: %v", err)}}
			}

			return nil
		},
		func(formID string, sse *datastar.ServerSentEventGenerator) {
			_ = ds.Send.HideDrawer(sse)
			_ = ds.Send.Toast(sse, ds.ToastSuccess, "Customer added successfully")
		},
	)
}
