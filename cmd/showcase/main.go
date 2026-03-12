package main

import (
	"crypto/rand"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx"
	"github.com/laenen-partners/dsx/cmd/showcase/internal/handlers"
	"github.com/laenen-partners/dsx/cmd/showcase/internal/pages"
	"github.com/laenen-partners/dsx/cmd/showcase/internal/static"
	"github.com/laenen-partners/dsx/pubsub/natspubsub"
	"github.com/laenen-partners/dsx/stream"
	"github.com/laenen-partners/dsx/ui"
	"github.com/laenen-partners/dsx/ui/calendar"
	"github.com/laenen-partners/dsx/ui/markdown"
	"github.com/laenen-partners/dsx/ui/moneyinput"
	"github.com/laenen-partners/dsx/ui/themecontroller"
	"github.com/laenen-partners/dsx/ui/validator"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "showcase",
		Short: "WebX component showcase",
	}

	root.AddCommand(serveCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func serveCmd() *cobra.Command {
	var (
		port int
		pro  bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the showcase HTTP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return serve(port, pro)
		},
	}

	cmd.Flags().IntVarP(&port, "port", "p", 3000, "port to listen on (0 for random)")

	return cmd
}

func serve(port int, pro bool) error {
	readmeBytes, err := os.ReadFile("README.md")
	if err != nil {
		slog.Warn("could not read README.md", "error", err)
	}
	readme := string(readmeBytes)

	gettingStartedBytes, err := os.ReadFile("docs/getting-started.md")
	if err != nil {
		slog.Warn("could not read docs/getting-started.md", "error", err)
	}
	gettingStarted := string(gettingStartedBytes)

	streamSpecBytes, err := os.ReadFile("docs/stream-spec.md")
	if err != nil {
		slog.Warn("could not read docs/stream-spec.md", "error", err)
	}
	streamSpec := string(streamSpecBytes)

	// Start embedded NATS server (in-process, no TCP port).
	ns, err := server.NewServer(&server.Options{DontListen: true})
	if err != nil {
		return fmt.Errorf("creating NATS server: %w", err)
	}
	ns.Start()
	defer ns.Shutdown()
	if !ns.ReadyForConnections(4 * time.Second) {
		return fmt.Errorf("NATS server not ready")
	}

	nc, err := nats.Connect(ns.ClientURL(), nats.InProcessServer(ns))
	if err != nil {
		return fmt.Errorf("connecting to NATS: %w", err)
	}
	defer nc.Close()

	broker := stream.NewBroker(natspubsub.New(nc))
	slog.Info("embedded NATS started (in-process)")

	// Generate random HMAC secret for CSRF.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generating CSRF secret: %w", err)
	}

	r := chi.NewRouter()

	// Session + CSRF middleware (cookie-based)
	r.Use(dsx.Middleware(dsx.MiddlewareConfig{
		Secret: secret,
		Secure: false, // development mode
	}))
	r.Use(dsx.SecurityHeadersMiddleware())

	// Set base path and stream URL on every request
	const basePath = "/showcase"
	streamURL := basePath + "/stream"
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wxctx := dsx.FromContext(r.Context())
			wxctx.BasePath = basePath
			wxctx.StreamURL = streamURL
			next.ServeHTTP(w, r.WithContext(wxctx.WithContext(r.Context())))
		})
	})

	// Serve static files (css, js) at /assets/
	staticFS, _ := fs.Sub(static.Static, "static")
	r.Handle("/assets/*", cacheControl(http.StripPrefix("/assets/", http.FileServerFS(staticFS))))

	r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Pages
	r.Get("/", templ.Handler(pages.Home(readme)).ServeHTTP)
	r.Get("/getting-started", templ.Handler(pages.GettingStarted(gettingStarted)).ServeHTTP)
	r.Get("/components/button", templ.Handler(pages.Buttons()).ServeHTTP)
	r.Get("/components/card", templ.Handler(pages.Cards()).ServeHTTP)
	r.Get("/components/drawer", templ.Handler(pages.Drawers()).ServeHTTP)
	r.Get("/components/accordion", templ.Handler(pages.Accordions()).ServeHTTP)
	r.Get("/components/ai-chat", templ.Handler(pages.AIChats()).ServeHTTP)
	r.Get("/components/alert", templ.Handler(pages.Alerts()).ServeHTTP)
	r.Get("/components/avatar", templ.Handler(pages.Avatars()).ServeHTTP)
	r.Get("/components/calendar", templ.Handler(pages.Calendars()).ServeHTTP)
	r.Get("/components/chat", templ.Handler(pages.Chats()).ServeHTTP)
	r.Get("/components/badge", templ.Handler(pages.Badges()).ServeHTTP)
	r.Get("/components/carousel", templ.Handler(pages.Carousels()).ServeHTTP)
	r.Get("/components/breadcrumbs", templ.Handler(pages.Breadcrumbs()).ServeHTTP)
	r.Get("/components/calendar-advanced", templ.Handler(pages.CalendarAdvanced()).ServeHTTP)
	r.Get("/components/dock", templ.Handler(pages.Docks()).ServeHTTP)
	r.Get("/components/drawer-advanced", templ.Handler(pages.DrawersAdvanced()).ServeHTTP)
	r.Get("/components/dropdown", templ.Handler(pages.Dropdowns()).ServeHTTP)
	r.Get("/components/fab", templ.Handler(pages.Fabs()).ServeHTTP)
	r.Get("/components/fieldset", templ.Handler(pages.Fieldsets()).ServeHTTP)
	r.Get("/components/footer", templ.Handler(pages.Footers()).ServeHTTP)
	r.Get("/components/file-input", templ.Handler(pages.FileInputs()).ServeHTTP)
	r.Get("/components/filter", templ.Handler(pages.Filters()).ServeHTTP)
	r.Get("/components/label", templ.Handler(pages.Labels()).ServeHTTP)
	r.Get("/components/hover-gallery", templ.Handler(pages.HoverGalleries()).ServeHTTP)
	r.Get("/components/indicator", templ.Handler(pages.Indicators()).ServeHTTP)
	r.Get("/components/join", templ.Handler(pages.Joins()).ServeHTTP)
	r.Get("/components/kbd", templ.Handler(pages.Kbds()).ServeHTTP)
	r.Get("/components/link", templ.Handler(pages.Links()).ServeHTTP)
	r.Get("/components/list", templ.Handler(pages.Lists()).ServeHTTP)
	r.Get("/components/loading", templ.Handler(pages.Loadings()).ServeHTTP)
	r.Get("/components/menu", templ.Handler(pages.Menus()).ServeHTTP)
	r.Get("/components/modal", templ.Handler(pages.Modals()).ServeHTTP)
	r.Get("/components/radio", templ.Handler(pages.Radios()).ServeHTTP)
	r.Get("/components/range", templ.Handler(pages.RangeInputs()).ServeHTTP)
	r.Get("/components/rating", templ.Handler(pages.Ratings()).ServeHTTP)
	r.Get("/components/progress", templ.Handler(pages.Progresses()).ServeHTTP)
	r.Get("/components/radial-progress", templ.Handler(pages.RadialProgresses()).ServeHTTP)
	r.Get("/components/mockup-code", templ.Handler(pages.MockupCodes()).ServeHTTP)
	r.Get("/components/navbar", templ.Handler(pages.Navbars()).ServeHTTP)
	r.Get("/components/pagination", templ.Handler(pages.Paginations()).ServeHTTP)
	r.Get("/components/stat", templ.Handler(pages.Stats()).ServeHTTP)
	r.Get("/components/status", templ.Handler(pages.Statuses()).ServeHTTP)
	r.Get("/components/steps", templ.Handler(pages.Stepss()).ServeHTTP)
	r.Get("/components/select", templ.Handler(pages.SelectInputs()).ServeHTTP)
	r.Get("/components/separator", templ.Handler(pages.Separators()).ServeHTTP)
	r.Get("/components/skeleton", templ.Handler(pages.Skeletons()).ServeHTTP)
	r.Get("/components/stream", templ.Handler(pages.Stream(streamSpec)).ServeHTTP)
	r.Get("/components/tab", templ.Handler(pages.Tabs()).ServeHTTP)
	r.Get("/components/table", templ.Handler(pages.Tables()).ServeHTTP)
	r.Get("/components/textarea", templ.Handler(pages.Textareas()).ServeHTTP)
	r.Get("/components/text-rotate", templ.Handler(pages.TextRotates()).ServeHTTP)
	r.Get("/components/timeline", templ.Handler(pages.Timelines()).ServeHTTP)
	r.Get("/components/toast", templ.Handler(pages.Toasts()).ServeHTTP)
	r.Get("/components/toggle", templ.Handler(pages.Toggles()).ServeHTTP)
	r.Get("/components/tooltip", templ.Handler(pages.Tooltips()).ServeHTTP)
	r.Get("/components/theme-controller", templ.Handler(pages.ThemeControllers()).ServeHTTP)
	r.Get("/components/validator", templ.Handler(pages.Validators()).ServeHTTP)
	r.Get("/components/markdown", templ.Handler(pages.Markdowns()).ServeHTTP)
	r.Get("/components/money", templ.Handler(pages.Moneys()).ServeHTTP)
	r.Get("/components/money-input", templ.Handler(pages.MoneyInputs()).ServeHTTP)
	r.Get("/components/stack", templ.Handler(pages.Stacks()).ServeHTTP)
	r.Get("/components/form", templ.Handler(pages.Forms()).ServeHTTP)
	r.Get("/components/file-upload", templ.Handler(pages.FileUploads()).ServeHTTP)
	r.Get("/components/json-view", templ.Handler(pages.JSONViews()).ServeHTTP)
	r.Get("/components/sse-sdk", templ.Handler(pages.ModalAdvanced()).ServeHTTP)
	r.Get("/components/code-view", templ.Handler(pages.CodeViews()).ServeHTTP)
	r.Get("/components/sparkline", templ.Handler(pages.Sparklines()).ServeHTTP)
	r.Get("/components/briefing", templ.Handler(pages.Briefings()).ServeHTTP)
	r.Get("/components/scroll-strip", templ.Handler(pages.ScrollStrips()).ServeHTTP)
	r.Get("/components/feed", templ.Handler(pages.Feeds()).ServeHTTP)
	r.Get("/components/feed-item", templ.Handler(pages.FeedItems()).ServeHTTP)
	r.Get("/components/command-bar", templ.Handler(pages.CommandBars()).ServeHTTP)
	r.Get("/components/yaml-tree", templ.Handler(pages.YamlTrees()).ServeHTTP)
	r.Get("/examples/butler", templ.Handler(pages.Butler()).ServeHTTP)
	r.Get("/examples/customers", templ.Handler(pages.Customers()).ServeHTTP)

	// SSE API endpoints
	h := handlers.New(broker)
	r.Route(basePath, func(r chi.Router) {
		r.Get("/stream", broker.Handler())
		r.Post("/stream/subscribe", broker.SubscribeHandler())
		ui.RegisterRoutes(r,
			calendar.Route(),
			themecontroller.Route(false),
			markdown.Route(),
			moneyinput.DecimalRoute(),
			moneyinput.MoneyRoute(),
			validator.Route(),
		)
		r.Get("/parse/money-restricted", moneyinput.MoneyHandler("USD", "EUR"))
		h.RegisterRoutes(r)
	})

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", port, err)
	}

	dsMode := "open-source"
	if pro {
		dsMode = "pro"
	}
	slog.Info("server started", "address", fmt.Sprintf("http://localhost:%d", ln.Addr().(*net.TCPAddr).Port), "datastar", dsMode)

	return http.Serve(ln, r)
}

// cacheControl wraps a handler to set Cache-Control: no-cache so browsers
// revalidate via ETags on each request.
func cacheControl(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}
