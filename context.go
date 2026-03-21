// Provides context for dsx templates
package dsx

import (
	"context"
	"fmt"
)

// Context carries request-scoped state through the middleware chain and into
// templ components. Retrieve it with FromContext.
type Context struct {
	SessionID string
	CSRFToken string
	Theme     string
	BasePath  string // prefix for all SSE handler routes (e.g. "/showcase")
	StreamURL string // URL for the reactive SSE stream endpoint (e.g. "/showcase/stream")
}

// NewContext returns an empty Context.
func NewContext() *Context {
	return &Context{}
}

type ctxKey struct{}

// FromContext extracts the dsx Context from a context.Context.
// If none is present, a new empty Context is returned.
func FromContext(ctx context.Context) *Context {
	if wctx, ok := ctx.Value(ctxKey{}).(*Context); ok {
		return wctx
	}
	return NewContext()
}

// WithContext stores the dsx Context into a context.Context.
func (c *Context) WithContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// APIPath returns the full path for a component SSE handler by prepending
// the BasePath. For example, with BasePath "/showcase" and path "/calendar/navigate",
// it returns "/showcase/calendar/navigate".
func (c *Context) APIPath(path string) string {
	return c.BasePath + path
}

// Post returns a Datastar expression that performs a POST request to the given URL.
func Post(url string) string {
	return fmt.Sprintf("@post('%s')", url)
}
