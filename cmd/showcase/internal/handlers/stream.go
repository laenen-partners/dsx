package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/stream"
	"github.com/starfederation/datastar-go/datastar"
)

type streamHandlers struct {
	broker  *stream.Broker
	counter atomic.Int64
}

func newStreamHandlers(broker *stream.Broker) *streamHandlers {
	return &streamHandlers{broker: broker}
}

func (s *streamHandlers) register(r chi.Router) {
	r.Get("/stream/counter", s.getCounter())
	r.Get("/stream/increment", s.increment())
	r.Get("/stream/decrement", s.decrement())
	r.Get("/stream/reset", s.reset())
}

func (s *streamHandlers) getCounter() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		count := s.counter.Load()
		_ = sse.PatchElements(
			fmt.Sprintf(`<span id="stream-counter-value" class="text-6xl font-bold tabular-nums">%d</span>`, count),
		)
	}
}

func (s *streamHandlers) increment() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.counter.Add(1)
		if err := s.broker.Invalidate("counter:shared"); err != nil {
			slog.Error("invalidating counter stream", "error", err)
		}
		datastar.NewSSE(w, r)
	}
}

func (s *streamHandlers) decrement() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.counter.Add(-1)
		if err := s.broker.Invalidate("counter:shared"); err != nil {
			slog.Error("decrementing counter stream", "error", err)
		}
		datastar.NewSSE(w, r)
	}
}

func (s *streamHandlers) reset() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.counter.Store(0)
		if err := s.broker.Invalidate("counter:shared"); err != nil {
			slog.Error("resetting counter stream", "error", err)
		}
		datastar.NewSSE(w, r)
	}
}
