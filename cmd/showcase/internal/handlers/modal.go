package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx"
	"github.com/laenen-partners/dsx/cmd/showcase/internal/pages"
	"github.com/laenen-partners/dsx/ds"
	"github.com/starfederation/datastar-go/datastar"
)

type modalHandlers struct{}

func newModalHandlers() *modalHandlers {
	return &modalHandlers{}
}

func (h *modalHandlers) register(r chi.Router) {
	r.Get("/modal/show", h.showModal())
	r.Get("/modal/show-wide", h.showWideModal())
	r.Get("/modal/confirm", h.showConfirm())
	r.Get("/modal/confirm-danger", h.showDangerConfirm())
	r.Post("/modal/confirmed", h.confirmed())
	r.Get("/modal/patch", h.patchContent())
	r.Get("/modal/redirect", h.redirect())
	r.Get("/modal/download", h.download())
}

func (h *modalHandlers) showModal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Modal(sse, pages.ModalContent())
	}
}

func (h *modalHandlers) showWideModal() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Modal(sse, pages.ModalContentWide(), ds.WithModalMaxWidth("max-w-2xl"))
	}
}

func (h *modalHandlers) showConfirm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wxctx := dsx.FromContext(r.Context())
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Confirm(sse, "Are you sure you want to proceed with this action?",
			wxctx.APIPath("/modal/confirmed"),
		)
	}
}

func (h *modalHandlers) showDangerConfirm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wxctx := dsx.FromContext(r.Context())
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Confirm(sse, "This will permanently delete all data. This action cannot be undone.",
			wxctx.APIPath("/modal/confirmed"),
			ds.WithConfirmTitle("Danger Zone"),
			ds.WithConfirmLabel("Delete Everything"),
			ds.WithConfirmClass("btn btn-error"),
		)
	}
}

func (h *modalHandlers) confirmed() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Toast(sse, ds.ToastSuccess, "Action confirmed!")
	}
}

func (h *modalHandlers) patchContent() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		timestamp := time.Now().Format("15:04:05")
		_ = ds.Send.Patch(sse, pages.PatchedContent(timestamp))
	}
}

func (h *modalHandlers) redirect() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wxctx := dsx.FromContext(r.Context())
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Redirect(sse, wxctx.BasePath+"/")
	}
}

func (h *modalHandlers) download() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wxctx := dsx.FromContext(r.Context())
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Download(sse, wxctx.APIPath("/modal/export.csv"), "export.csv")
	}
}
