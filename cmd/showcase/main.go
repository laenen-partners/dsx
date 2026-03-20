package main

import (
	"context"
	"log"
	"log/slog"
	"os"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/cmd/showcase/internal/handlers"
	"github.com/laenen-partners/dsx/cmd/showcase/internal/pages"
	"github.com/laenen-partners/dsx/showcase"
	"github.com/laenen-partners/dsx/stream"
	"github.com/laenen-partners/dsx/ui"
	"github.com/laenen-partners/dsx/ui/calendar"
	"github.com/laenen-partners/dsx/ui/markdown"
	"github.com/laenen-partners/dsx/ui/moneyinput"
	"github.com/laenen-partners/dsx/ui/themecontroller"
	"github.com/laenen-partners/dsx/ui/validator"
	"github.com/laenen-partners/pubsub"
)

func main() {
	readmeBytes, err := os.ReadFile("README.md")
	if err != nil {
		slog.Warn("could not read README.md", "error", err)
	}

	gettingStartedBytes, err := os.ReadFile("docs/getting-started.md")
	if err != nil {
		slog.Warn("could not read docs/getting-started.md", "error", err)
	}

	streamSpecBytes, err := os.ReadFile("docs/stream-spec.md")
	if err != nil {
		slog.Warn("could not read docs/stream-spec.md", "error", err)
	}

	if err := showcase.Run(showcase.Config{
		Port: 3333,
		Identities: []showcase.Identity{
			{Name: "Admin", TenantID: "showcase", WorkspaceID: "ws-1", PrincipalID: "admin-1", Roles: []string{"admin"}},
			{Name: "Viewer", TenantID: "showcase", WorkspaceID: "ws-1", PrincipalID: "viewer-1", Roles: []string{"viewer"}},
		},
		Pages: map[string]templ.Component{
			"/":                             pages.Home(string(readmeBytes)),
			"/getting-started":              pages.GettingStarted(string(gettingStartedBytes)),
			"/components/button":            pages.Buttons(),
			"/components/card":              pages.Cards(),
			"/components/drawer":            pages.Drawers(),
			"/components/accordion":         pages.Accordions(),
			"/components/ai-chat":           pages.AIChats(),
			"/components/alert":             pages.Alerts(),
			"/components/avatar":            pages.Avatars(),
			"/components/calendar":          pages.Calendars(),
			"/components/chat":              pages.Chats(),
			"/components/badge":             pages.Badges(),
			"/components/carousel":          pages.Carousels(),
			"/components/breadcrumbs":       pages.Breadcrumbs(),
			"/components/calendar-advanced": pages.CalendarAdvanced(),
			"/components/dock":              pages.Docks(),
			"/components/drawer-advanced":   pages.DrawersAdvanced(),
			"/components/dropdown":          pages.Dropdowns(),
			"/components/fab":               pages.Fabs(),
			"/components/fieldset":          pages.Fieldsets(),
			"/components/footer":            pages.Footers(),
			"/components/file-input":        pages.FileInputs(),
			"/components/filter":            pages.Filters(),
			"/components/label":             pages.Labels(),
			"/components/hover-gallery":     pages.HoverGalleries(),
			"/components/indicator":         pages.Indicators(),
			"/components/join":              pages.Joins(),
			"/components/kbd":               pages.Kbds(),
			"/components/link":              pages.Links(),
			"/components/list":              pages.Lists(),
			"/components/loading":           pages.Loadings(),
			"/components/menu":              pages.Menus(),
			"/components/modal":             pages.Modals(),
			"/components/radio":             pages.Radios(),
			"/components/range":             pages.RangeInputs(),
			"/components/rating":            pages.Ratings(),
			"/components/progress":          pages.Progresses(),
			"/components/radial-progress":   pages.RadialProgresses(),
			"/components/mockup-code":       pages.MockupCodes(),
			"/components/navbar":            pages.Navbars(),
			"/components/pagination":        pages.Paginations(),
			"/components/stat":              pages.Stats(),
			"/components/status":            pages.Statuses(),
			"/components/steps":             pages.Stepss(),
			"/components/select":            pages.SelectInputs(),
			"/components/separator":         pages.Separators(),
			"/components/skeleton":          pages.Skeletons(),
			"/components/stream":            pages.Stream(string(streamSpecBytes)),
			"/components/tab":               pages.Tabs(),
			"/components/table":             pages.Tables(),
			"/components/textarea":          pages.Textareas(),
			"/components/text-rotate":       pages.TextRotates(),
			"/components/timeline":          pages.Timelines(),
			"/components/toast":             pages.Toasts(),
			"/components/toggle":            pages.Toggles(),
			"/components/tooltip":           pages.Tooltips(),
			"/components/theme-controller":  pages.ThemeControllers(),
			"/components/validator":         pages.Validators(),
			"/components/markdown":          pages.Markdowns(),
			"/components/money":             pages.Moneys(),
			"/components/money-input":       pages.MoneyInputs(),
			"/components/stack":             pages.Stacks(),
			"/components/form":              pages.Forms(),
			"/components/file-upload":       pages.FileUploads(),
			"/components/json-view":         pages.JSONViews(),
			"/components/sse-sdk":           pages.ModalAdvanced(),
			"/components/code-view":         pages.CodeViews(),
			"/components/sparkline":         pages.Sparklines(),
			"/components/briefing":          pages.Briefings(),
			"/components/scroll-strip":      pages.ScrollStrips(),
			"/components/feed":              pages.Feeds(),
			"/components/feed-item":         pages.FeedItems(),
			"/components/command-bar":       pages.CommandBars(),
			"/components/yaml-tree":         pages.YamlTrees(),
			"/examples/butler":              pages.Butler(),
			"/examples/customers":           pages.Customers(),
		},
		Setup: func(ctx context.Context, r chi.Router, bus *pubsub.Bus, relay *stream.Relay) error {
			h := handlers.New(bus, relay)
			r.Route("/showcase", func(r chi.Router) {
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
			return nil
		},
	}); err != nil {
		log.Fatal(err)
	}
}
