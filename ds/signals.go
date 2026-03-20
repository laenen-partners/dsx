package ds

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/starfederation/datastar-go/datastar"
	"github.com/valyala/bytebufferpool"
)

// ReadSignals reads namespaced signals from a Datastar request.
// The componentID is sanitized (hyphens → underscores) to match the JS namespace.
// dest must be a pointer to a struct with json tags matching the signal shape.
//
// Call this BEFORE datastar.NewSSE() — SSE creation consumes the request body.
//
//	var signals commandbar.CommandBarSignals
//	if err := ds.ReadSignals("my-bar", r, &signals); err != nil { ... }
//	input := signals.Text
func ReadSignals(componentID string, r *http.Request, dest any) error {
	sanitizedID := strings.ReplaceAll(componentID, "-", "_")

	// Read the raw request body or from form field (multipart).
	var raw []byte
	contentType := r.Header.Get("Content-Type")
	isMultipart := strings.HasPrefix(contentType, "multipart/form-data")

	if r.Method == "GET" {
		dsJSON := r.URL.Query().Get("datastar")
		if dsJSON == "" {
			return nil
		}
		raw = []byte(dsJSON)
	} else if isMultipart {
		// Datastar sends signals as a JSON string in the "datastar" field when using multipart.
		dsJSON := r.FormValue("datastar")
		if dsJSON == "" {
			return nil
		}
		raw = []byte(dsJSON)
	} else {
		buf := bytebufferpool.Get()
		defer bytebufferpool.Put(buf)
		if _, err := buf.ReadFrom(r.Body); err != nil {
			return fmt.Errorf("read signals for %q: read body: %w", componentID, err)
		}
		raw = buf.Bytes()
	}

	// Decode the top-level JSON object into raw messages per key.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("read signals for %q: unmarshal top-level: %w", componentID, err)
	}

	// Extract and unmarshal the namespaced portion into dest.
	nsRaw, ok := top[sanitizedID]
	if !ok {
		return nil // namespace not present — leave dest at zero values
	}
	if err := json.Unmarshal(nsRaw, dest); err != nil {
		return fmt.Errorf("read signals for %q: unmarshal namespace: %w", componentID, err)
	}
	return nil
}

// ReadAndSSE reads namespaced signals from a Datastar request and then creates
// the SSE writer. This enforces the correct ordering — ReadSignals must happen
// before NewSSE because SSE creation consumes the request body.
//
//	sse, err := ds.ReadAndSSE("my-form", w, r, &signals)
//	if err != nil { ... }
//	sse.MarshalAndPatchSignals(...)
func ReadAndSSE(componentID string, w http.ResponseWriter, r *http.Request, dest any) (*datastar.ServerSentEventGenerator, error) {
	if err := ReadSignals(componentID, r, dest); err != nil {
		return nil, err
	}
	return datastar.NewSSE(w, r), nil
}

// ReadRaw reads all signal namespaces from a Datastar request as raw JSON.
// Returns a map keyed by namespace (sanitized component ID) with raw JSON values.
//
// Call this BEFORE datastar.NewSSE() — SSE creation consumes the request body.
//
// This is useful when you need to discover which namespace was sent
// (e.g. with filterSignals) rather than reading a known component ID.
//
//	var raw map[string]json.RawMessage
//	if err := ds.ReadRaw(r, &raw); err != nil { ... }
func ReadRaw(r *http.Request, dest *map[string]json.RawMessage) error {
	var raw []byte
	if r.Method == "GET" {
		dsJSON := r.URL.Query().Get("datastar")
		if dsJSON == "" {
			return nil
		}
		raw = []byte(dsJSON)
	} else {
		buf := bytebufferpool.Get()
		defer bytebufferpool.Put(buf)
		if _, err := buf.ReadFrom(r.Body); err != nil {
			return fmt.Errorf("read raw signals: read body: %w", err)
		}
		raw = buf.Bytes()
	}

	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("read raw signals: unmarshal: %w", err)
	}
	return nil
}
