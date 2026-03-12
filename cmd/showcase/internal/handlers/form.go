package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/ds"
	"github.com/laenen-partners/dsx/ui/form"
	"github.com/laenen-partners/dsx/utils/validators"
	"github.com/starfederation/datastar-go/datastar"
)

type formHandlers struct{}

func newFormHandlers() *formHandlers {
	return &formHandlers{}
}

func (f *formHandlers) register(r chi.Router) {
	r.Post("/form/login", f.login())
	r.Post("/form/contact", f.contact())
	r.Post("/form/error-demo", f.errorDemo())
}

type loginFormSignals struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (f *formHandlers) login() http.HandlerFunc {
	return form.Handler(
		loginFormSignals{},
		func(formID string, r *http.Request) []form.FieldError {
			var signals loginFormSignals
			if err := ds.ReadSignals(formID, r, &signals); err != nil {
				return []form.FieldError{{Field: "error", Message: "Failed to read form data"}}
			}

			var errs []form.FieldError
			if signals.Email == "" {
				errs = append(errs, form.FieldError{Field: "email_error", Message: "Email is required"})
			} else {
				res := validators.Email(signals.Email, false)
				if !res.Valid {
					errs = append(errs, form.FieldError{Field: "email_error", Message: res.Error})
				}
			}
			if signals.Password == "" {
				errs = append(errs, form.FieldError{Field: "password_error", Message: "Password is required"})
			} else if len(signals.Password) < 8 {
				errs = append(errs, form.FieldError{Field: "password_error", Message: "Password must be at least 8 characters"})
			}
			return errs
		},
		func(formID string, sse *datastar.ServerSentEventGenerator) {
			sanitizedID := strings.ReplaceAll(formID, "-", "_")
			_ = sse.MarshalAndPatchSignals(map[string]any{
				sanitizedID: map[string]any{
					"success": "Login successful!",
				},
			})
		},
	)
}

type errorDemoSignals struct {
	Username string `json:"username"`
}

func (f *formHandlers) errorDemo() http.HandlerFunc {
	return form.Handler(
		errorDemoSignals{},
		func(formID string, r *http.Request) []form.FieldError {
			var signals errorDemoSignals
			if err := ds.ReadSignals(formID, r, &signals); err != nil {
				return []form.FieldError{{Field: "error", Message: "Failed to read form data"}}
			}

			if signals.Username == "" {
				return []form.FieldError{{Field: "username_error", Message: "Username is required"}}
			}

			// Simulate a server error
			return []form.FieldError{{Field: "error", Message: "Service temporarily unavailable. Please try again later."}}
		},
		nil,
	)
}

type contactFormSignals struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Message string `json:"message"`
}

func (f *formHandlers) contact() http.HandlerFunc {
	return form.Handler(
		contactFormSignals{},
		func(formID string, r *http.Request) []form.FieldError {
			var signals contactFormSignals
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
			if signals.Message == "" {
				errs = append(errs, form.FieldError{Field: "message_error", Message: "Message is required"})
			}
			return errs
		},
		func(formID string, sse *datastar.ServerSentEventGenerator) {
			sanitizedID := strings.ReplaceAll(formID, "-", "_")
			_ = sse.MarshalAndPatchSignals(map[string]any{
				sanitizedID: map[string]any{
					"success": "Message sent successfully!",
				},
			})
		},
	)
}
