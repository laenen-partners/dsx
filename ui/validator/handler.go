package validator

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/utils/validators"
	"github.com/starfederation/datastar-go/datastar"
)

// Validation identifies a built-in server-side validation type.
type Validation string

const (
	// Email validates email format (regex-based).
	Email Validation = "email"
	// EmailMX validates email format and checks MX records.
	EmailMX Validation = "email-mx"
	// IBANValidation validates IBAN format and checksum.
	IBANValidation Validation = "iban"
	// SWIFTValidation validates SWIFT/BIC code format.
	SWIFTValidation Validation = "swift"
)

// validationInputTypes maps validation types to their default HTML input type.
// If not listed, defaults to "text".
var validationInputTypes = map[Validation]InputType{
	Email:   TypeEmail,
	EmailMX: TypeEmail,
}

// validationPath returns the handler path for a built-in validation type.
func (v Validation) path() string {
	return "/validate/" + string(v)
}

// Path returns the handler path for a built-in validation type.
// Use with dsx.Context.APIPath to build the full URL.
func (v Validation) Path() string {
	return v.path()
}

// builtinValidators maps validation types to their ValidateFunc.
var builtinValidators = map[Validation]ValidateFunc{
	Email: func(value string) Result {
		r := validators.Email(value, false)
		return Result{Valid: r.Valid, Error: r.Error}
	},
	EmailMX: func(value string) Result {
		r := validators.Email(value, true)
		return Result{Valid: r.Valid, Error: r.Error}
	},
	IBANValidation: func(value string) Result {
		r := validators.IBAN(value)
		return Result{Valid: r.Valid, Error: r.Error}
	},
	SWIFTValidation: func(value string) Result {
		r := validators.SWIFT(value)
		return Result{Valid: r.Valid, Error: r.Error}
	},
}

// Route returns a RouteOption that registers all built-in validators.
func Route() func(chi.Router) {
	return func(r chi.Router) {
		for v, fn := range builtinValidators {
			r.Get(v.path(), Handler(fn))
		}
	}
}

// Result holds the outcome of a validation check.
type Result struct {
	Valid bool
	Error string
}

// ValidateFunc validates a string value and returns a Result.
type ValidateFunc func(value string) Result

// Handler returns an http.HandlerFunc for a single ValidateFunc.
// The component ID is passed as a query parameter "id".
//
// Mount each validator at its own path:
//
//	r.Get("/validate/email", validator.Handler(emailValidator))
//	r.Get("/validate/phone", validator.Handler(phoneValidator))
//
// The component references this path via the ValidateURL prop.
func Handler(fn ValidateFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		componentID := r.URL.Query().Get("id")
		if componentID == "" {
			http.Error(w, "missing id query parameter", http.StatusBadRequest)
			return
		}

		// Signals arrive namespaced: {"component_id": {"value": "...", ...}}
		sanitizedID := strings.ReplaceAll(componentID, "-", "_")
		wrapper := map[string]inputSignals{}
		if err := datastar.ReadSignals(r, &wrapper); err != nil {
			http.Error(w, fmt.Sprintf("read signals: %v", err), http.StatusBadRequest)
			return
		}

		store, ok := wrapper[sanitizedID]
		if !ok {
			http.Error(w, fmt.Sprintf("missing signals for %q", sanitizedID), http.StatusBadRequest)
			return
		}

		result := fn(store.Value)

		sse := datastar.NewSSE(w, r)
		_ = sse.MarshalAndPatchSignals(map[string]any{
			sanitizedID: map[string]any{
				"valid": result.Valid,
				"error": result.Error,
			},
		})
	}
}

// inputSignals is the signal shape sent by the client.
type inputSignals struct {
	Value string `json:"value"`
	Valid bool   `json:"valid"`
	Error string `json:"error"`
}
