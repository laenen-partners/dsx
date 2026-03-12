package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/laenen-partners/dsx/ds"
	"github.com/laenen-partners/dsx/ui/aichat"
	"github.com/laenen-partners/dsx/ui/commandbar"
	"github.com/starfederation/datastar-go/datastar"
)

type aichatHandlers struct{}

func newAIChatHandlers() *aichatHandlers {
	return &aichatHandlers{}
}

func (h *aichatHandlers) register(r chi.Router) {
	r.Post("/aichat/send", h.send(demoChatID, h.readAIChatInput))
	r.Post("/aichat/send-combined", h.send(combinedChatID, h.readCommandBarInput))
	r.Post("/aichat/action", h.action())
	r.Post("/aichat/upload", h.action())
	r.Post("/aichat/voice", h.action())
}

const (
	demoChatID     = "demo-aichat"
	combinedChatID = "demo-combined"
	combinedBarID  = "combined-bar"
)

// inputReader extracts the user's text from the request signals.
type inputReader func(r *http.Request) (string, error)

func (h *aichatHandlers) readAIChatInput(r *http.Request) (string, error) {
	var signals aichat.AIChatSignals
	if err := aichat.ReadSignals(demoChatID, r, &signals); err != nil {
		return "", fmt.Errorf("read aichat signals: %w", err)
	}
	return signals.Input, nil
}

func (h *aichatHandlers) readCommandBarInput(r *http.Request) (string, error) {
	var signals commandbar.CommandBarSignals
	if err := ds.ReadSignals(combinedBarID, r, &signals); err != nil {
		return "", fmt.Errorf("read commandbar signals: %w", err)
	}
	return signals.Text, nil
}

func (h *aichatHandlers) send(chatID string, readInput inputReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		text, err := readInput(r)
		if err != nil {
			slog.Error("aichat: failed to read input", "error", err)
			http.Error(w, "failed to read input", http.StatusBadRequest)
			return
		}

		input := strings.TrimSpace(text)
		if input == "" {
			return
		}

		sse := datastar.NewSSE(w, r)
		chat := aichat.Chat(sse, chatID)

		_ = chat.UserMessage(input)
		_ = chat.ShowTyping()
		time.Sleep(800 * time.Millisecond)
		_ = chat.HideTyping()

		submitURL := "/showcase/aichat/send"
		if chatID == combinedChatID {
			submitURL = "/showcase/aichat/send-combined"
		}
		actionURL := "/showcase/aichat/action"

		lower := strings.ToLower(input)
		switch {
		case contains(lower, "cancel", "subscription"):
			_ = chat.Append(subscriptionResponse(chatID, submitURL))

		case contains(lower, "plan", "date"):
			_ = chat.Append(dateNightResponse(chatID, submitURL))

		case contains(lower, "buy", "needs", "football", "shoes", "boots"):
			_ = chat.Append(shoppingResponse(input, actionURL))

		case contains(lower, "netflix", "spotify", "youtube", "show all"):
			_ = chat.Append(subscriptionDetailResponse(lower, chatID, submitURL))

		case contains(lower, "friday", "saturday", "week"):
			_ = chat.Append(dateConfirmResponse(lower))

		default:
			_ = chat.Append(defaultResponse(input, actionURL))
		}
	}
}

func (h *aichatHandlers) action() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sse := datastar.NewSSE(w, r)
		_ = ds.Send.Toast(sse, ds.ToastSuccess, "Added to inbox!")
	}
}

func contains(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}
