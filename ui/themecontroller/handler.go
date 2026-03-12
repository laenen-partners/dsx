package themecontroller

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx"
	"github.com/starfederation/datastar-go/datastar"
)

// SetThemePath is the route path for the theme persistence endpoint.
const SetThemePath = "/theme"

// Route returns a RouteOption that registers the theme persistence handler.
// The secure flag controls the Secure attribute on the theme cookie.
func Route(secure bool) func(chi.Router) {
	return func(r chi.Router) {
		r.Post(SetThemePath, SetThemeHandler(secure))
	}
}

// SetThemeHandler returns an HTTP handler that persists the selected theme
// to a cookie. It reads the theme from Datastar signals and sets the
// dsx_theme cookie.
func SetThemeHandler(secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Datastar signals are namespaced by component ID, so we read the
		// raw map and search for the "theme" value in any nested object.
		var signals map[string]any
		if err := datastar.ReadSignals(r, &signals); err != nil {
			http.Error(w, fmt.Sprintf("reading signals: %v", err), http.StatusBadRequest)
			return
		}

		theme := extractTheme(signals)
		if theme == "" {
			http.Error(w, "theme signal not found", http.StatusBadRequest)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "dsx_theme",
			Value:    theme,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   secure,
		})

		_ = dsx.FromContext(r.Context()) // ensure context exists

		sse := datastar.NewSSE(w, r)
		_ = sse.MarshalAndPatchSignals(signals)
	}
}

// extractTheme finds the "theme" value in a potentially nested signal map.
// Datastar namespaces signals under the component ID, e.g.:
// {"tc_toggle": {"theme": "dark"}}
func extractTheme(signals map[string]any) string {
	// Check top-level first
	if v, ok := signals["theme"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}

	// Check nested objects
	for _, v := range signals {
		nested, err := toMap(v)
		if err != nil {
			continue
		}
		if theme, ok := nested["theme"]; ok {
			if s, ok := theme.(string); ok {
				return s
			}
		}
	}
	return ""
}

// toMap converts a value to map[string]any, handling both map types and
// json.RawMessage that datastar.ReadSignals may produce.
func toMap(v any) (map[string]any, error) {
	switch m := v.(type) {
	case map[string]any:
		return m, nil
	case json.RawMessage:
		var result map[string]any
		if err := json.Unmarshal(m, &result); err != nil {
			return nil, err
		}
		return result, nil
	default:
		return nil, fmt.Errorf("not a map: %T", v)
	}
}
