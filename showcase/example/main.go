// Command example runs a minimal showcase demonstrating fragments, stream
// auto-refresh, and identity switching.
//
// Run:
//
//	go run ./showcase/example
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/ds"
	"github.com/laenen-partners/dsx/showcase"
	"github.com/laenen-partners/dsx/stream"
	"github.com/laenen-partners/pubsub"
	"github.com/starfederation/datastar-go/datastar"
)

func main() {
	var counter atomic.Int64

	if err := showcase.Run(showcase.Config{
		Port: 3333,
		Identities: []showcase.Identity{
			{Name: "Admin", TenantID: "t1", PrincipalID: "admin-1", Roles: []string{"admin"}},
			{Name: "Viewer", TenantID: "t1", PrincipalID: "viewer-1", Roles: []string{"viewer"}},
		},
		Pages: map[string]templ.Component{
			"/": homePage(),
		},
		Setup: func(ctx context.Context, r chi.Router, bus *pubsub.Bus, relay *stream.Relay) error {
			// Fragment: renders the current counter value.
			r.Get("/showcase/counter", func(w http.ResponseWriter, r *http.Request) {
				sse := datastar.NewSSE(w, r)
				_ = ds.Send.Patch(sse, counterFragment(counter.Load()))
			})

			// Action: increments the counter and publishes a change notification.
			r.Post("/showcase/counter/increment", func(w http.ResponseWriter, r *http.Request) {
				counter.Add(1)
				if err := bus.NotifyUpdated(r.Context(), "counter", "shared"); err != nil {
					http.Error(w, fmt.Sprintf("publish: %v", err), http.StatusInternalServerError)
					return
				}
				sse := datastar.NewSSE(w, r)
				_ = ds.Send.Toast(sse, ds.ToastSuccess, fmt.Sprintf("Counter is now %d", counter.Load()))
			})

			// Background ticker: auto-increments every 5s to show live updates.
			go func() {
				ticker := time.NewTicker(5 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						counter.Add(1)
						_ = bus.NotifyUpdated(ctx, "counter", "shared")
					}
				}
			}()

			return nil
		},
	}); err != nil {
		log.Fatal(err)
	}
}
