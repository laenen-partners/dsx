package moneyinput

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
)

type decimalHandlerSignals struct {
	Value  string `json:"value"`
	Amount string `json:"amount"`
	Error  string `json:"error"`
}

type moneyHandlerSignals struct {
	Value    string `json:"value"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Error    string `json:"error"`
}

// DecimalPath is the standard handler path for decimal parsing.
const DecimalPath = "/parse/decimal"

// MoneyPath is the standard handler path for money parsing.
const MoneyPath = "/parse/money"

// DecimalRoute returns a RouteOption that registers the decimal parser handler.
func DecimalRoute() func(chi.Router) {
	return func(r chi.Router) {
		r.Get(DecimalPath, DecimalHandler())
	}
}

// MoneyRoute returns a RouteOption that registers the money parser handler.
// If allowedCurrencies is provided, only those currencies are accepted.
func MoneyRoute(allowedCurrencies ...string) func(chi.Router) {
	return func(r chi.Router) {
		r.Get(MoneyPath, MoneyHandler(allowedCurrencies...))
	}
}

// DecimalHandler returns an http.HandlerFunc that parses a numeric value
// (supporting shorthand like 5k, 1.5M) and patches the signals with the
// formatted result.
//
// Mount at a dedicated path:
//
//	r.Get(moneyinput.DecimalPath, moneyinput.DecimalHandler())
func DecimalHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		componentID := r.URL.Query().Get("id")
		if componentID == "" {
			http.Error(w, "missing id query parameter", http.StatusBadRequest)
			return
		}

		sanitizedID := strings.ReplaceAll(componentID, "-", "_")
		wrapper := map[string]decimalHandlerSignals{}
		if err := datastar.ReadSignals(r, &wrapper); err != nil {
			http.Error(w, fmt.Sprintf("read signals: %v", err), http.StatusBadRequest)
			return
		}

		store, ok := wrapper[sanitizedID]
		if !ok {
			http.Error(w, fmt.Sprintf("missing signals for %q", sanitizedID), http.StatusBadRequest)
			return
		}

		result := ParseAmount(store.Value)

		patch := map[string]any{
			"amount": "",
			"error":  "",
		}
		if !result.Valid {
			patch["error"] = result.Error
		} else if store.Value != "" {
			patch["amount"] = FormatAmount(result.Value)
		}

		sse := datastar.NewSSE(w, r)
		_ = sse.MarshalAndPatchSignals(map[string]any{
			sanitizedID: patch,
		})
	}
}

// MoneyHandler returns an http.HandlerFunc that parses a money value
// (e.g., "USD 5k", "100 EUR") and patches the signals with the formatted
// amount and detected currency.
//
// If allowedCurrencies is provided, only those currencies are accepted.
//
// Mount at a dedicated path:
//
//	r.Get(moneyinput.MoneyPath, moneyinput.MoneyHandler("USD", "EUR"))
func MoneyHandler(allowedCurrencies ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		componentID := r.URL.Query().Get("id")
		if componentID == "" {
			http.Error(w, "missing id query parameter", http.StatusBadRequest)
			return
		}

		sanitizedID := strings.ReplaceAll(componentID, "-", "_")
		wrapper := map[string]moneyHandlerSignals{}
		if err := datastar.ReadSignals(r, &wrapper); err != nil {
			http.Error(w, fmt.Sprintf("read signals: %v", err), http.StatusBadRequest)
			return
		}

		store, ok := wrapper[sanitizedID]
		if !ok {
			http.Error(w, fmt.Sprintf("missing signals for %q", sanitizedID), http.StatusBadRequest)
			return
		}

		result := ParseMoney(store.Value, allowedCurrencies)

		patch := map[string]any{
			"amount":   "",
			"currency": "",
			"error":    "",
		}
		if !result.Valid {
			patch["error"] = result.Error
		} else if store.Value != "" {
			patch["amount"] = FormatAmount(result.Value)
			patch["currency"] = result.Currency
		}

		sse := datastar.NewSSE(w, r)
		_ = sse.MarshalAndPatchSignals(map[string]any{
			sanitizedID: patch,
		})
	}
}
