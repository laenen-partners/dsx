package jsonview

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// highlight takes a raw string and returns syntax-highlighted HTML.
// If the input is valid JSON, it formats and colorizes it.
// If not, it returns the original value escaped for HTML.
func highlight(raw string) string {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return escapeHTML(raw)
	}
	var b strings.Builder
	renderValue(&b, parsed, 0)
	return b.String()
}

func renderValue(b *strings.Builder, v any, indent int) {
	switch val := v.(type) {
	case map[string]any:
		renderObject(b, val, indent)
	case []any:
		renderArray(b, val, indent)
	case string:
		fmt.Fprintf(b, `<span class="text-success">"%s"</span>`, escapeHTML(val))
	case float64:
		fmt.Fprintf(b, `<span class="text-info">%s</span>`, formatNumber(val))
	case bool:
		fmt.Fprintf(b, `<span class="text-warning">%t</span>`, val)
	case nil:
		b.WriteString(`<span class="text-error">null</span>`)
	}
}

func renderObject(b *strings.Builder, obj map[string]any, indent int) {
	if len(obj) == 0 {
		b.WriteString("{}")
		return
	}

	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	b.WriteString("{\n")
	for i, k := range keys {
		writeIndent(b, indent+1)
		fmt.Fprintf(b, `<span class="text-secondary">"%s"</span>`, escapeHTML(k))
		b.WriteString(": ")
		renderValue(b, obj[k], indent+1)
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	writeIndent(b, indent)
	b.WriteString("}")
}

func renderArray(b *strings.Builder, arr []any, indent int) {
	if len(arr) == 0 {
		b.WriteString("[]")
		return
	}

	b.WriteString("[\n")
	for i, item := range arr {
		writeIndent(b, indent+1)
		renderValue(b, item, indent+1)
		if i < len(arr)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	writeIndent(b, indent)
	b.WriteString("]")
}

func writeIndent(b *strings.Builder, level int) {
	for range level {
		b.WriteString("  ")
	}
}

func formatNumber(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
