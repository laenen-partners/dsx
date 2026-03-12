package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/cmd/showcase/internal/pages"
	"github.com/laenen-partners/dsx/ds"
	"github.com/starfederation/datastar-go/datastar"
)

type drawerHandlers struct{}

func newDrawerHandlers() *drawerHandlers {
	return &drawerHandlers{}
}

func (h *drawerHandlers) register(r chi.Router) {
	r.Get("/drawer/project/{id}", h.showProject())
}

var mockProjects = map[string]pages.Project{
	"1": {
		ID:          "1",
		Name:        "WebX Framework",
		Description: "A full-stack component library combining Go, Templ, Tailwind CSS, DaisyUI, and Datastar into a cohesive development experience.",
		Status:      "active",
		Progress:    78,
		TeamSize:    4,
		Tech:        []string{"Go", "Templ", "Tailwind", "DaisyUI", "Datastar"},
	},
	"2": {
		ID:          "2",
		Name:        "Neon Database",
		Description: "Serverless Postgres with instant branching, autoscaling, and a generous free tier for development.",
		Status:      "active",
		Progress:    92,
		TeamSize:    12,
		Tech:        []string{"Postgres", "Rust", "Go", "React"},
	},
	"3": {
		ID:          "3",
		Name:        "Data Pipeline",
		Description: "Real-time ETL pipeline processing analytics events from multiple sources into a unified data warehouse.",
		Status:      "paused",
		Progress:    45,
		TeamSize:    3,
		Tech:        []string{"Go", "Kafka", "ClickHouse", "dbt"},
	},
	"4": {
		ID:          "4",
		Name:        "Mobile App",
		Description: "Cross-platform mobile client providing a native experience for the core platform features.",
		Status:      "active",
		Progress:    63,
		TeamSize:    6,
		Tech:        []string{"React Native", "TypeScript", "Expo"},
	},
	"5": {
		ID:          "5",
		Name:        "Auth Service",
		Description: "OAuth2 and OpenID Connect identity provider handling authentication and authorization for all services.",
		Status:      "completed",
		Progress:    100,
		TeamSize:    2,
		Tech:        []string{"Go", "OIDC", "JWT", "Redis"},
	},
	"6": {
		ID:          "6",
		Name:        "Monitoring",
		Description: "Comprehensive observability stack with metrics collection, alerting, and distributed tracing.",
		Status:      "planning",
		Progress:    12,
		TeamSize:    2,
		Tech:        []string{"Prometheus", "Grafana", "OpenTelemetry"},
	},
}

func (h *drawerHandlers) showProject() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		project, ok := mockProjects[id]
		if !ok {
			http.NotFound(w, r)
			return
		}

		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Drawer(sse, pages.DrawerDetail(project))
	}
}
