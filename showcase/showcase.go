// Package showcase provides a reusable server for previewing DSX UI fragments.
//
// It handles the boilerplate that every module showcase needs: CSRF middleware,
// security headers, static asset serving (CSS + DataStar JS), mock identity
// with switchable roles, and graceful shutdown.
//
// Usage:
//
//	showcase.Run(showcase.Config{
//	    Port: 3333,
//	    Identities: []showcase.Identity{
//	        {Name: "Admin", TenantID: "t1", PrincipalID: "admin-1", Roles: []string{"admin"}},
//	        {Name: "Viewer", TenantID: "t1", PrincipalID: "viewer-1", Roles: []string{"viewer"}},
//	    },
//	    Setup: func(ctx context.Context, r chi.Router, bus *pubsub.Bus, relay *stream.Relay) error {
//	        r.Get("/fragments/jobs", h.JobList())
//	        return nil
//	    },
//	    Pages: map[string]templ.Component{
//	        "/": myShowcasePage(),
//	    },
//	})
package showcase

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx"
	"github.com/laenen-partners/dsx/ds"
	"github.com/laenen-partners/dsx/stream"
	"github.com/laenen-partners/identity"
	"github.com/laenen-partners/pubsub"
	"github.com/laenen-partners/pubsub/chanpubsub"
	"github.com/starfederation/datastar-go/datastar"
)

// Themes is the list of available DaisyUI themes for the showcase theme dropdown.
var Themes = []string{
	"silk",
	"light",
	"dark",
	"cupcake",
	"bumblebee",
	"emerald",
	"corporate",
	"retro",
	"cyberpunk",
	"valentine",
	"garden",
	"lofi",
	"pastel",
	"fantasy",
	"wireframe",
	"cmyk",
	"autumn",
	"acid",
	"lemonade",
	"nord",
	"caramellatte",
	"synthwave",
	"halloween",
	"forest",
	"aqua",
	"night",
	"coffee",
	"dim",
	"sunset",
	"dracula",
	"business",
	"luxury",
	"black",
	"abyss",
	"winter",
}

// Identity defines a switchable user persona for the showcase.
type Identity struct {
	Name          string // display name (e.g. "Admin", "Viewer")
	TenantID      string
	WorkspaceID   string
	PrincipalID   string
	PrincipalType identity.PrincipalType // defaults to PrincipalUser
	Roles         []string
}

// CustomContext holds user-editable identity and dsx context fields,
// persisted as a JSON cookie.
type CustomContext struct {
	// Identity fields
	TenantID      string `json:"tenant_id"`
	WorkspaceID   string `json:"workspace_id"`
	PrincipalID   string `json:"principal_id"`
	PrincipalType string `json:"principal_type"`
	Roles         string `json:"roles"` // comma-separated

	// DSX context fields
	Theme     string `json:"theme"`
	BasePath  string `json:"base_path"`
	StreamURL string `json:"stream_url"`
}

// Config configures the showcase server.
type Config struct {
	// Port to listen on. Default: 3333.
	Port int

	// Identities available for switching. The first one is active by default.
	// If empty, a default admin identity is used.
	Identities []Identity

	// Setup is called after middleware is applied. The Bus and Relay are backed
	// by an in-process chanpubsub. Use the Bus to publish change notifications
	// and the Relay handles SSE subscriptions automatically.
	Setup func(ctx context.Context, r chi.Router, bus *pubsub.Bus, relay *stream.Relay) error

	// Pages maps URL paths to page titles. Each page is automatically wrapped
	// in the showcase layout (navbar with identity switcher).
	// The page body should be provided via ContentForPath, or leave empty
	// for a page that only contains auto-loaded fragments.
	Pages map[string]templ.Component

	// SimplePage maps URL paths to page titles. Each title is wrapped in
	// showcase.Page() with the identity switcher navbar. Use this for pages
	// whose content is entirely loaded via fragment endpoints.
	SimplePages map[string]string
}

// Run starts the showcase server and blocks until interrupted.
func Run(cfg Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// PORT env var overrides the configured port (including PORT=0 for random).
	if v, ok := os.LookupEnv("PORT"); ok {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	} else if cfg.Port == 0 {
		cfg.Port = 3333
	}
	if len(cfg.Identities) == 0 {
		cfg.Identities = []Identity{{
			Name:        "Admin",
			TenantID:    "showcase",
			WorkspaceID: "ws-1",
			PrincipalID: "admin-1",
			Roles:       []string{"admin"},
		}}
	}
	for i := range cfg.Identities {
		if cfg.Identities[i].WorkspaceID == "" {
			cfg.Identities[i].WorkspaceID = "ws-1"
		}
		if cfg.Identities[i].PrincipalType == "" {
			cfg.Identities[i].PrincipalType = identity.PrincipalUser
		}
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("showcase: generate CSRF secret: %w", err)
	}

	// In-process pub/sub, bus, and stream relay for reactive fragments.
	ps := chanpubsub.New()
	defer func() { _ = ps.Close(context.Background()) }()
	defaultID := cfg.Identities[0]
	bus := pubsub.NewBus(ps, "showcase", pubsub.WithScope(defaultID.TenantID, defaultID.WorkspaceID))
	relay := stream.New(ps, showcasePatternResolver(defaultID.TenantID, defaultID.WorkspaceID))

	r := chi.NewRouter()

	r.Use(dsx.Middleware(dsx.MiddlewareConfig{
		Secret: secret,
		Secure: false,
	}))
	r.Use(dsx.SecurityHeadersMiddleware())

	// Set BasePath and StreamURL on every request so the watch worker can connect.
	r.Use(showcaseContextMiddleware())

	// Identity + custom context middleware.
	r.Use(contextMiddleware(cfg.Identities))

	// Serve DSX static assets.
	staticFS, _ := fs.Sub(dsx.Static, "static")
	r.Handle("/assets/*", http.StripPrefix("/assets/", http.FileServerFS(staticFS)))

	// Stream SSE endpoint.
	r.Get("/showcase/stream", relay.Handler())

	// Identity switcher endpoints.
	r.Get("/showcase/identities", identityListHandler(cfg.Identities))
	r.Post("/showcase/identity/{index}", identitySwitchHandler(cfg.Identities))

	// Context editor endpoints.
	r.Get("/showcase/context/edit", contextEditHandler(cfg.Identities))
	r.Post("/showcase/context", contextSaveHandler(cfg.Identities))
	r.Post("/showcase/context/reset", contextResetHandler(cfg.Identities))

	// Register user pages.
	for path, component := range cfg.Pages {
		r.Get(path, templ.Handler(component).ServeHTTP)
	}
	for path, title := range cfg.SimplePages {
		r.Get(path, templ.Handler(Page(title)).ServeHTTP)
	}

	// Let the caller register fragment routes.
	if cfg.Setup != nil {
		if err := cfg.Setup(ctx, r, bus, relay); err != nil {
			return fmt.Errorf("showcase: setup: %w", err)
		}
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return fmt.Errorf("showcase: listen on port %d: %w", cfg.Port, err)
	}

	slog.Info("showcase started", "address", fmt.Sprintf("http://localhost:%d", ln.Addr().(*net.TCPAddr).Port))

	errCh := make(chan error, 1)
	go func() { errCh <- http.Serve(ln, r) }()

	select {
	case <-ctx.Done():
		slog.Info("showcase: shutting down...")
		_ = ln.Close()
		return nil
	case err := <-errCh:
		return err
	}
}

const (
	identityCookie = "showcase_identity"
	contextCookie  = "showcase_context"
)

// readCustomContext reads the custom context cookie. Returns nil if not set.
func readCustomContext(r *http.Request) *CustomContext {
	c, err := r.Cookie(contextCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var cc CustomContext
	if err := json.Unmarshal(b, &cc); err != nil {
		return nil
	}
	return &cc
}

// writeCustomContext sets the custom context cookie.
func writeCustomContext(w http.ResponseWriter, cc *CustomContext) {
	b, err := json.Marshal(cc)
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     contextCookie,
		Value:    base64.RawURLEncoding.EncodeToString(b),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearCustomContext removes the custom context cookie.
func clearCustomContext(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     contextCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// showcaseContextMiddleware sets default BasePath and StreamURL on the dsx
// context so the watch worker and component API paths work out of the box.
func showcaseContextMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dsxCtx := dsx.FromContext(r.Context())
			if dsxCtx.BasePath == "" {
				dsxCtx.BasePath = "/showcase"
			}
			if dsxCtx.StreamURL == "" {
				dsxCtx.StreamURL = "/showcase/stream"
			}
			next.ServeHTTP(w, r.WithContext(dsxCtx.WithContext(r.Context())))
		})
	}
}

// contextMiddleware reads identity from either the custom context cookie or
// the preset index cookie, and applies dsx context overrides.
func contextMiddleware(identities []Identity) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Check for custom context first.
			if cc := readCustomContext(r); cc != nil {
				pt := identity.PrincipalType(cc.PrincipalType)
				if pt == "" {
					pt = identity.PrincipalUser
				}
				roles := splitRoles(cc.Roles)
				id, err := identity.New(
					cc.TenantID,
					cc.WorkspaceID,
					cc.PrincipalID,
					pt,
					"showcase",
					roles,
				)
				if err != nil {
					slog.Error("showcase: invalid custom identity", "error", err)
					http.Error(w, "invalid custom identity", http.StatusInternalServerError)
					return
				}
				ctx = identity.WithContext(ctx, id)

				// Apply dsx context overrides.
				dsxCtx := dsx.FromContext(ctx)
				if cc.Theme != "" {
					dsxCtx.Theme = cc.Theme
				}
				if cc.BasePath != "" {
					dsxCtx.BasePath = cc.BasePath
				}
				if cc.StreamURL != "" {
					dsxCtx.StreamURL = cc.StreamURL
				}
				ctx = dsxCtx.WithContext(ctx)

				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Fall back to preset identity.
			idx := 0
			if c, err := r.Cookie(identityCookie); err == nil {
				if n, err := strconv.Atoi(c.Value); err == nil && n >= 0 && n < len(identities) {
					idx = n
				}
			}
			persona := identities[idx]
			id, err := identity.New(
				persona.TenantID,
				persona.WorkspaceID,
				persona.PrincipalID,
				persona.PrincipalType,
				"showcase",
				persona.Roles,
			)
			if err != nil {
				slog.Error("showcase: invalid identity", "index", idx, "error", err)
				http.Error(w, "invalid showcase identity", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(identity.WithContext(ctx, id)))
		})
	}
}

// identitySwitchHandler sets the identity cookie, clears any custom context,
// and re-renders the switcher.
func identitySwitchHandler(identities []Identity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idxStr := chi.URLParam(r, "index")
		idx, err := strconv.Atoi(idxStr)
		if err != nil || idx < 0 || idx >= len(identities) {
			http.Error(w, "invalid identity index", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     identityCookie,
			Value:    strconv.Itoa(idx),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		clearCustomContext(w)
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Patch(sse, IdentitySwitcher(identities, idx, false))
	}
}

// identityListHandler renders the identity switcher fragment.
func identityListHandler(identities []Identity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hasCustom := readCustomContext(r) != nil
		idx := 0
		if !hasCustom {
			if c, err := r.Cookie(identityCookie); err == nil {
				if n, err := strconv.Atoi(c.Value); err == nil && n >= 0 && n < len(identities) {
					idx = n
				}
			}
		}
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Patch(sse, IdentitySwitcher(identities, idx, hasCustom))
	}
}

// currentContext builds a CustomContext from the current request state.
func currentContext(r *http.Request, identities []Identity) CustomContext {
	// If custom context cookie exists, use it.
	if cc := readCustomContext(r); cc != nil {
		return *cc
	}

	// Build from preset identity + dsx context.
	idx := 0
	if c, err := r.Cookie(identityCookie); err == nil {
		if n, err := strconv.Atoi(c.Value); err == nil && n >= 0 && n < len(identities) {
			idx = n
		}
	}
	persona := identities[idx]
	dsxCtx := dsx.FromContext(r.Context())

	return CustomContext{
		TenantID:      persona.TenantID,
		WorkspaceID:   persona.WorkspaceID,
		PrincipalID:   persona.PrincipalID,
		PrincipalType: string(persona.PrincipalType),
		Roles:         strings.Join(persona.Roles, ", "),
		Theme:         dsxCtx.Theme,
		BasePath:      dsxCtx.BasePath,
		StreamURL:     dsxCtx.StreamURL,
	}
}

// contextEditHandler opens the context editor drawer.
func contextEditHandler(identities []Identity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cc := currentContext(r, identities)
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Drawer(r.Context(), sse, ContextEditor(cc))
	}
}

// ContextEditorSignals matches the form signals for the context editor.
type ContextEditorSignals struct {
	TenantID      string `json:"tenant_id"`
	WorkspaceID   string `json:"workspace_id"`
	PrincipalID   string `json:"principal_id"`
	PrincipalType string `json:"principal_type"`
	Roles         string `json:"roles"`
	Theme         string `json:"theme"`
	BasePath      string `json:"base_path"`
	StreamURL     string `json:"stream_url"`
}

// contextSaveHandler saves the custom context and reloads the page.
func contextSaveHandler(identities []Identity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var signals ContextEditorSignals
		if err := ds.ReadSignals("ctx-editor", r, &signals); err != nil {
			http.Error(w, fmt.Sprintf("reading signals: %v", err), http.StatusBadRequest)
			return
		}

		cc := &CustomContext{
			TenantID:      strings.TrimSpace(signals.TenantID),
			WorkspaceID:   strings.TrimSpace(signals.WorkspaceID),
			PrincipalID:   strings.TrimSpace(signals.PrincipalID),
			PrincipalType: signals.PrincipalType,
			Roles:         signals.Roles,
			Theme:         strings.TrimSpace(signals.Theme),
			BasePath:      strings.TrimSpace(signals.BasePath),
			StreamURL:     strings.TrimSpace(signals.StreamURL),
		}

		// Validate identity fields.
		pt := identity.PrincipalType(cc.PrincipalType)
		if pt == "" {
			pt = identity.PrincipalUser
		}
		_, err := identity.New(cc.TenantID, cc.WorkspaceID, cc.PrincipalID, pt, "showcase", splitRoles(cc.Roles))
		if err != nil {
			sse := datastar.NewSSE(w, r)
			_ = ds.Send.Toast(sse, ds.ToastError, fmt.Sprintf("Invalid identity: %v", err))
			return
		}

		writeCustomContext(w, cc)
		clearPresetCookie(w)

		sse := datastar.NewSSE(w, r)
		_ = ds.Send.HideDrawer(sse)
		_ = ds.Send.Toast(sse, ds.ToastSuccess, "Context updated — reload to apply")
		_ = sse.ExecuteScript("setTimeout(() => window.location.reload(), 500)")
	}
}

// contextResetHandler clears the custom context and reloads.
func contextResetHandler(identities []Identity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clearCustomContext(w)
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.HideDrawer(sse)
		_ = ds.Send.Toast(sse, ds.ToastSuccess, "Context reset to preset")
		_ = sse.ExecuteScript("setTimeout(() => window.location.reload(), 500)")
	}
}

// clearPresetCookie removes the preset identity cookie.
func clearPresetCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     identityCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// showcasePatternResolver returns a PatternResolver that produces subscription
// patterns matching subjects published by [pubsub.Bus] with the given scope.
// The bus publishes to "{tenant}.{workspace}.change.{entity}.{entityID}.{action}",
// so the resolver produces "{tenant}.{workspace}.change.{domain}.>" for broad
// watches and "{tenant}.{workspace}.change.{domain}.{id}.>" for ID-scoped watches.
func showcasePatternResolver(tenant, workspace string) stream.PatternResolver {
	return func(_ context.Context, watch string) string {
		domain, entityID, hasID := strings.Cut(watch, ".")
		if !hasID || entityID == "" {
			return fmt.Sprintf("%s.%s.change.%s.>", tenant, workspace, domain)
		}
		return fmt.Sprintf("%s.%s.change.%s.%s.>", tenant, workspace, domain, entityID)
	}
}

// splitRoles splits a comma-separated roles string into a slice.
func splitRoles(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	roles := make([]string, 0, len(parts))
	for _, p := range parts {
		if r := strings.TrimSpace(p); r != "" {
			roles = append(roles, r)
		}
	}
	return roles
}
