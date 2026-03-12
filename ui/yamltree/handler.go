package yamltree

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/ds"
	"github.com/starfederation/datastar-go/datastar"
)

// Callback is called after every mutation with the component ID and updated data.
// The consuming app can persist, validate, or broadcast the change.
type Callback func(componentID string, data any) error

// RegisterHandlers mounts the edit/add/remove SSE handlers at basePath.
// getData returns the current YAML data (map[string]any).
// callback is invoked after each mutation.
func RegisterHandlers(mux chi.Router, basePath string, getData func() any, callback Callback) {
	mux.Post(basePath+"/edit", editHandler(basePath, getData, callback))
	mux.Post(basePath+"/add", addHandler(basePath, getData, callback))
	mux.Post(basePath+"/remove", removeHandler(basePath, getData, callback))
}

func editHandler(basePath string, getData func() any, callback Callback) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}

		var signals TreeSignals
		if err := ds.ReadSignals(id, r, &signals); err != nil {
			http.Error(w, fmt.Sprintf("read signals: %v", err), http.StatusBadRequest)
			return
		}

		if signals.EditingPath == "" {
			http.Error(w, "no editing path", http.StatusBadRequest)
			return
		}

		data := getData()
		if err := SetAtPath(data, signals.EditingPath, parseValue(signals.EditingValue)); err != nil {
			sse := datastar.NewSSE(w, r)
			_ = ds.Send.Toast(sse, ds.ToastError, fmt.Sprintf("Failed to update: %v", err))
			return
		}

		if callback != nil {
			if err := callback(id, data); err != nil {
				sse := datastar.NewSSE(w, r)
				_ = ds.Send.Toast(sse, ds.ToastError, fmt.Sprintf("Callback error: %v", err))
				return
			}
		}

		sse := datastar.NewSSE(w, r)
		patchTree(sse, id, basePath, data)
		clearEditSignals(sse, id)
	}
}

func addHandler(basePath string, getData func() any, callback Callback) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}

		var signals TreeSignals
		if err := ds.ReadSignals(id, r, &signals); err != nil {
			http.Error(w, fmt.Sprintf("read signals: %v", err), http.StatusBadRequest)
			return
		}

		if signals.AddKey == "" {
			sse := datastar.NewSSE(w, r)
			_ = ds.Send.Toast(sse, ds.ToastWarning, "Key name is required")
			return
		}

		data := getData()
		if err := AddAtPath(data, signals.AddParent, signals.AddKey, parseValue(signals.AddValue)); err != nil {
			sse := datastar.NewSSE(w, r)
			_ = ds.Send.Toast(sse, ds.ToastError, fmt.Sprintf("Failed to add: %v", err))
			return
		}

		if callback != nil {
			if err := callback(id, data); err != nil {
				sse := datastar.NewSSE(w, r)
				_ = ds.Send.Toast(sse, ds.ToastError, fmt.Sprintf("Callback error: %v", err))
				return
			}
		}

		sse := datastar.NewSSE(w, r)
		patchTree(sse, id, basePath, data)
		clearAddSignals(sse, id)
	}
}

func removeHandler(basePath string, getData func() any, callback Callback) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}

		path := r.URL.Query().Get("path")
		if path == "" {
			http.Error(w, "missing path", http.StatusBadRequest)
			return
		}

		// Read signals before SSE (consume request body)
		var signals TreeSignals
		_ = ds.ReadSignals(id, r, &signals)

		data := getData()
		newData, err := DeleteAtPath(data, path)
		if err != nil {
			sse := datastar.NewSSE(w, r)
			_ = ds.Send.Toast(sse, ds.ToastError, fmt.Sprintf("Failed to remove: %v", err))
			return
		}

		if callback != nil {
			if err := callback(id, newData); err != nil {
				sse := datastar.NewSSE(w, r)
				_ = ds.Send.Toast(sse, ds.ToastError, fmt.Sprintf("Callback error: %v", err))
				return
			}
		}

		sse := datastar.NewSSE(w, r)
		patchTree(sse, id, basePath, newData)
	}
}

func patchTree(sse *datastar.ServerSentEventGenerator, id, actionURL string, data any) {
	component := YamlTree(Props{
		ID:        id,
		Data:      data,
		ActionURL: actionURL,
	})
	_ = ds.Send.Patch(sse, component)
}

func clearEditSignals(sse *datastar.ServerSentEventGenerator, id string) {
	sanitized := strings.ReplaceAll(id, "-", "_")
	_ = sse.MarshalAndPatchSignals(map[string]any{
		sanitized: map[string]any{
			"editing_path":  "",
			"editing_value": "",
		},
	})
}

func clearAddSignals(sse *datastar.ServerSentEventGenerator, id string) {
	sanitized := strings.ReplaceAll(id, "-", "_")
	_ = sse.MarshalAndPatchSignals(map[string]any{
		sanitized: map[string]any{
			"add_parent": "",
			"add_key":    "",
			"add_value":  "",
		},
	})
}

// parseValue attempts to interpret a string as a typed YAML value.
func parseValue(s string) any {
	switch strings.ToLower(s) {
	case "true":
		return true
	case "false":
		return false
	case "null", "~", "":
		return nil
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
		return n
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err == nil {
		return f
	}
	return s
}
