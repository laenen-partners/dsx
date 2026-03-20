package handlers

import (
	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/stream"
	"github.com/laenen-partners/pubsub"
)

// Handlers wires all showcase SSE/API handlers.
type Handlers struct {
	form       *formHandlers
	upload     *uploadHandlers
	toast      *toastHandlers
	stream     *streamHandlers
	drawer     *drawerHandlers
	modal      *modalHandlers
	commandbar *commandbarHandlers
	aichat     *aichatHandlers
	yamltree   *yamltreeHandlers
	customer   *customerHandlers
}

func New(bus *pubsub.Bus, relay *stream.Relay) *Handlers {
	fileStore := newFileStore()
	return &Handlers{
		form:       newFormHandlers(),
		upload:     newUploadHandlers(fileStore),
		toast:      newToastHandlers(),
		stream:     newStreamHandlers(bus, relay),
		drawer:     newDrawerHandlers(),
		modal:      newModalHandlers(),
		commandbar: newCommandbarHandlers(),
		aichat:     newAIChatHandlers(),
		yamltree:   newYamlTreeHandlers(),
		customer:   newCustomerHandlers(bus),
	}
}

// RegisterRoutes mounts all API handlers onto the given router.
func (h *Handlers) RegisterRoutes(r chi.Router) {
	h.form.register(r)
	h.upload.register(r)
	h.toast.register(r)
	h.stream.register(r)
	h.drawer.register(r)
	h.modal.register(r)
	h.commandbar.register(r)
	h.aichat.register(r)
	h.yamltree.register(r)
	h.customer.register(r)
}
