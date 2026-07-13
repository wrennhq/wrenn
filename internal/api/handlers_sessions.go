package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/session"
)

type sessionsHandler struct {
	sessions *session.Service
}

func newSessionsHandler(svc *session.Service) *sessionsHandler {
	return &sessionsHandler{sessions: svc}
}

// List handles GET /v1/me/sessions — returns all active sessions for the
// current user, flagging the caller's own session as current.
func (h *sessionsHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	rows, err := h.sessions.ListForUser(r.Context(), ac.UserID)
	if err != nil {
		slog.Error("list sessions: db error", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}
	out := make([]sessionRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionRow{
			ID:         row.ID,
			UserAgent:  row.UserAgent,
			IPAddress:  row.IpAddress,
			CreatedAt:  row.CreatedAt.Time.UTC().Format(time.RFC3339),
			LastSeenAt: row.LastSeenAt.Time.UTC().Format(time.RFC3339),
			ExpiresAt:  row.ExpiresAt.Time.UTC().Format(time.RFC3339),
			Current:    row.ID == ac.SessionID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// Delete handles DELETE /v1/me/sessions/{id} — revokes a single session
// belonging to the current user. If the caller revokes their own session,
// their cookies are cleared on the response.
func (h *sessionsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	sid := chi.URLParam(r, "id")
	if sid == "" {
		writeErr(w, r, apperr.InvalidRequest.Msg("The session id is required."))
		return
	}
	if err := h.sessions.DeleteForUser(r.Context(), sid, ac.UserID); err != nil {
		slog.Error("delete session: db error", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}
	if sid == ac.SessionID {
		clearSessionCookies(w, isSecure(r))
	}
	w.WriteHeader(http.StatusNoContent)
}
