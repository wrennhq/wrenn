package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// sseTicket represents a short-lived opaque token that authenticates an SSE connection.
type sseTicket struct {
	teamID  string
	isAdmin bool
	expires time.Time
}

// SSETicketStore holds outstanding SSE tickets. Tickets are single-use and short-lived.
type SSETicketStore struct {
	mu      sync.Mutex
	tickets map[string]sseTicket
}

// NewSSETicketStore creates a ticket store and starts a background cleanup
// goroutine that exits when ctx is cancelled. Callers should pass the
// server's root context so a clean shutdown drops the goroutine cleanly.
func NewSSETicketStore(ctx context.Context) *SSETicketStore {
	s := &SSETicketStore{tickets: make(map[string]sseTicket)}
	go s.cleanup(ctx)
	return s
}

func (s *SSETicketStore) cleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for k, t := range s.tickets {
				if now.After(t.expires) {
					delete(s.tickets, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

// Issue creates a new ticket valid for 30 seconds.
func (s *SSETicketStore) Issue(teamID string, isAdmin bool) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(b)
	s.mu.Lock()
	s.tickets[ticket] = sseTicket{teamID: teamID, isAdmin: isAdmin, expires: time.Now().Add(30 * time.Second)}
	s.mu.Unlock()
	return ticket, nil
}

// Redeem validates and consumes a ticket (single-use).
func (s *SSETicketStore) Redeem(ticket string) (teamID string, isAdmin bool, ok bool) {
	s.mu.Lock()
	t, exists := s.tickets[ticket]
	if exists {
		delete(s.tickets, ticket)
	}
	s.mu.Unlock()
	if !exists || time.Now().After(t.expires) {
		return "", false, false
	}
	return t.teamID, t.isAdmin, true
}

const sseKeepaliveInterval = 30 * time.Second

type sseHandler struct {
	broker  *SSEBroker
	tickets *SSETicketStore
}

func newSSEHandler(broker *SSEBroker, tickets *SSETicketStore) *sseHandler {
	return &sseHandler{broker: broker, tickets: tickets}
}

// IssueToken handles POST /v1/events/token — exchanges a JWT/API key for a
// short-lived, single-use SSE ticket. The ticket is passed as ?ticket= on
// the EventSource URL, avoiding long-lived tokens in server logs.
func (h *sseHandler) IssueToken(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	ticket, err := h.tickets.Issue(id.FormatTeamID(ac.TeamID), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to issue ticket")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"ticket": ticket}); err != nil {
		slog.Warn("sse token encode failed", "error", err)
	}
}

// IssueAdminToken handles POST /v1/admin/events/token — admin variant.
func (h *sseHandler) IssueAdminToken(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	ticket, err := h.tickets.Issue(id.FormatTeamID(ac.TeamID), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to issue ticket")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"ticket": ticket}); err != nil {
		slog.Warn("sse token encode failed", "error", err)
	}
}

// Stream handles GET /v1/events/stream — authenticates via ?ticket= query param.
func (h *sseHandler) Stream(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		// Allow direct header auth for SDK consumers.
		ac, ok := auth.FromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "ticket or API key required")
			return
		}
		h.serveSSE(w, r, id.FormatTeamID(ac.TeamID), false)
		return
	}
	teamID, _, ok := h.tickets.Redeem(ticket)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired ticket")
		return
	}
	h.serveSSE(w, r, teamID, false)
}

// AdminStream handles GET /v1/admin/events/stream — authenticates via ?ticket=.
func (h *sseHandler) AdminStream(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "ticket required")
		return
	}
	teamID, isAdmin, ok := h.tickets.Redeem(ticket)
	if !ok || !isAdmin {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired ticket")
		return
	}
	h.serveSSE(w, r, teamID, true)
}

func (h *sseHandler) serveSSE(w http.ResponseWriter, r *http.Request, teamID string, isAdmin bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	subID, ch := h.broker.Subscribe(teamID, isAdmin)
	defer h.broker.Unsubscribe(subID)

	// Send initial connected event.
	fmt.Fprintf(w, "event: connected\ndata: {\"message\":\"connected\"}\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.EventType, msg.Data)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
