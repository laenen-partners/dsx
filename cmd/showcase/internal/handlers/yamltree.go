package handlers

import (
	"log/slog"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/ui/yamltree"
)

type yamltreeHandlers struct {
	mu   sync.Mutex
	data map[string]any
}

func newYamlTreeHandlers() *yamltreeHandlers {
	return &yamltreeHandlers{
		data: map[string]any{
			"server": map[string]any{
				"host": "0.0.0.0",
				"port": 3000,
				"tls": map[string]any{
					"enabled": true,
					"cert":    "/etc/ssl/cert.pem",
					"key":     "/etc/ssl/key.pem",
				},
			},
			"database": map[string]any{
				"driver": "postgres",
				"host":   "localhost",
				"port":   5432,
				"name":   "myapp",
				"pool": map[string]any{
					"max_open": 25,
					"max_idle": 5,
				},
			},
			"logging": map[string]any{
				"level":  "info",
				"format": "json",
			},
			"features": map[string]any{
				"signup":      true,
				"maintenance": false,
			},
		},
	}
}

func (h *yamltreeHandlers) register(r chi.Router) {
	yamltree.RegisterHandlers(r, "/yaml-tree", h.getData, h.onChange)
}

func (h *yamltreeHandlers) getData() any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.data
}

func (h *yamltreeHandlers) onChange(componentID string, data any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := data.(map[string]any); ok {
		h.data = m
	}
	slog.Info("yaml tree updated", "component", componentID)
	return nil
}
