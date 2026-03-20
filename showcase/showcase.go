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
//	    Setup: func(r chi.Router) error {
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
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx"
	"github.com/laenen-partners/dsx/ds"
	"github.com/laenen-partners/identity"
	"github.com/starfederation/datastar-go/datastar"
)

// Identity defines a switchable user persona for the showcase.
type Identity struct {
	Name          string // display name (e.g. "Admin", "Viewer")
	TenantID      string
	WorkspaceID   string
	PrincipalID   string
	PrincipalType identity.PrincipalType // defaults to PrincipalUser
	Roles         []string
}

// Config configures the showcase server.
type Config struct {
	// Port to listen on. Default: 3333.
	Port int

	// Identities available for switching. The first one is active by default.
	// If empty, a default admin identity is used.
	Identities []Identity

	// Setup is called after middleware is applied. Register your fragment
	// routes and any initialization (migrations, seeding) here.
	Setup func(ctx context.Context, r chi.Router) error

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

	if cfg.Port == 0 {
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

	r := chi.NewRouter()

	r.Use(dsx.Middleware(dsx.MiddlewareConfig{
		Secret: secret,
		Secure: false,
	}))
	r.Use(dsx.SecurityHeadersMiddleware())

	// Identity middleware — reads selected persona from cookie.
	r.Use(identityMiddleware(cfg.Identities))

	// Serve DSX static assets.
	staticFS, _ := fs.Sub(dsx.Static, "static")
	r.Handle("/assets/*", http.StripPrefix("/assets/", http.FileServerFS(staticFS)))

	// Identity switcher endpoints.
	r.Get("/showcase/identities", identityListHandler(cfg.Identities))
	r.Post("/showcase/identity/{index}", identitySwitchHandler(cfg.Identities))

	// Register user pages.
	for path, component := range cfg.Pages {
		r.Get(path, templ.Handler(component).ServeHTTP)
	}
	for path, title := range cfg.SimplePages {
		r.Get(path, templ.Handler(Page(title)).ServeHTTP)
	}

	// Let the caller register fragment routes.
	if cfg.Setup != nil {
		if err := cfg.Setup(ctx, r); err != nil {
			return fmt.Errorf("showcase: setup: %w", err)
		}
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return fmt.Errorf("showcase: listen on port %d: %w", cfg.Port, err)
	}

	slog.Info("showcase started", "address", fmt.Sprintf("http://localhost:%d", cfg.Port))

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

const identityCookie = "showcase_identity"

// identityMiddleware reads the selected identity index from a cookie and
// injects it into context.
func identityMiddleware(identities []Identity) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				persona.Roles,
			)
			if err != nil {
				slog.Error("showcase: invalid identity", "index", idx, "error", err)
				http.Error(w, "invalid showcase identity", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(identity.WithContext(r.Context(), id)))
		})
	}
}

// identitySwitchHandler sets the identity cookie and re-renders the switcher.
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
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Patch(sse, IdentitySwitcher(identities, idx))
	}
}

// identityListHandler renders the identity switcher fragment.
func identityListHandler(identities []Identity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx := 0
		if c, err := r.Cookie(identityCookie); err == nil {
			if n, err := strconv.Atoi(c.Value); err == nil && n >= 0 && n < len(identities) {
				idx = n
			}
		}
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Patch(sse, IdentitySwitcher(identities, idx))
	}
}
