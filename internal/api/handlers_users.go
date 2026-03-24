package api

import (
	"net/http"
	"strings"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/service"
)

type usersHandler struct {
	svc *service.TeamService
}

func newUsersHandler(svc *service.TeamService) *usersHandler {
	return &usersHandler{svc: svc}
}

// Search handles GET /v1/users/search?email=<prefix>
// Returns up to 10 users whose email starts with the given prefix.
// The prefix must contain "@" to scope searches and prevent broad enumeration.
func (h *usersHandler) Search(w http.ResponseWriter, r *http.Request) {
	auth.MustFromContext(r.Context()) // ensure authenticated

	prefix := strings.TrimSpace(r.URL.Query().Get("email"))
	if !strings.Contains(prefix, "@") {
		writeError(w, http.StatusBadRequest, "invalid_request", "email prefix must contain '@'")
		return
	}

	results, err := h.svc.SearchUsersByEmailPrefix(r.Context(), prefix)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	type userResult struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}
	resp := make([]userResult, len(results))
	for i, u := range results {
		resp[i] = userResult{UserID: u.ID, Email: u.Email}
	}
	writeJSON(w, http.StatusOK, resp)
}
