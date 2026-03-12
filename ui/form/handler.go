package form

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/laenen-partners/dsx/ds"
	"github.com/starfederation/datastar-go/datastar"
)

// FieldError represents a validation error for a specific form field.
type FieldError struct {
	// Field is the signal name (e.g. "email_error").
	Field string
	// Message is the error text shown to the user.
	Message string
}

// SubmitFunc processes a form submission.
// It receives the form ID and the raw request, and returns field errors.
// Return nil or empty slice for success.
type SubmitFunc func(formID string, r *http.Request) []FieldError

// errorFieldsFromSignals derives error field names from a signals struct.
// For each field with a json tag, it appends "_error" to create the
// corresponding error signal name.
//
//	type loginSignals struct {
//	    Email    string `json:"email"`
//	    Password string `json:"password"`
//	}
//	// → ["email_error", "password_error"]
func errorFieldsFromSignals(signals any) []string {
	t := reflect.TypeOf(signals)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	var fields []string
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		// Strip json options (e.g. "name,omitempty" → "name").
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		fields = append(fields, name+"_error")
	}
	return fields
}

// Handler returns an http.HandlerFunc that processes form submissions via SSE.
// The signals parameter declares the form's field shape — error signal names
// are derived automatically by appending "_error" to each json tag.
// On every submit, all derived error fields are cleared before applying actual
// errors, preventing stale errors from persisting across resubmissions.
//
// Mount at your form's Action path:
//
//	r.Post("/auth/login", form.Handler(loginFormSignals{}, loginValidator, onSuccess))
func Handler(signals any, validate SubmitFunc, onSuccess func(formID string, sse *datastar.ServerSentEventGenerator)) http.HandlerFunc {
	errorFields := errorFieldsFromSignals(signals)

	return func(w http.ResponseWriter, r *http.Request) {
		formID := r.URL.Query().Get("id")
		if formID == "" {
			http.Error(w, "missing id query parameter", http.StatusBadRequest)
			return
		}

		// Validate BEFORE creating the SSE — ReadSignals reads the
		// request body which is consumed once NewSSE flushes headers.
		errors := validate(formID, r)

		sanitizedID := strings.ReplaceAll(formID, "-", "_")
		sse := datastar.NewSSE(w, r)

		if len(errors) > 0 {
			// Start with all error fields cleared, then overwrite with actual errors.
			patch := map[string]any{
				"submitting": false,
			}
			for _, f := range errorFields {
				patch[f] = ""
			}
			for _, e := range errors {
				if e.Field == "error" {
					_ = ds.Send.Toast(sse, ds.ToastError, e.Message)
				} else {
					patch[e.Field] = e.Message
				}
			}
			_ = sse.MarshalAndPatchSignals(map[string]any{
				sanitizedID: patch,
			})
			return
		}

		// Clear submitting and all error fields on success.
		successPatch := map[string]any{
			"submitting": false,
		}
		for _, f := range errorFields {
			successPatch[f] = ""
		}
		_ = sse.MarshalAndPatchSignals(map[string]any{
			sanitizedID: successPatch,
		})

		if onSuccess != nil {
			onSuccess(formID, sse)
		}
	}
}
