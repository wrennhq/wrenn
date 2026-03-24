package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/service"
)

type teamHandler struct {
	svc *service.TeamService
}

func newTeamHandler(svc *service.TeamService) *teamHandler {
	return &teamHandler{svc: svc}
}

// teamResponse is the JSON shape for a team.
type teamResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	CreatedAt string `json:"created_at"`
}

// teamWithRoleResponse includes the calling user's role.
type teamWithRoleResponse struct {
	teamResponse
	Role string `json:"role"`
}

type memberResponse struct {
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at,omitempty"`
}

func teamToResponse(t db.Team) teamResponse {
	resp := teamResponse{
		ID:   t.ID,
		Name: t.Name,
		Slug: t.Slug,
	}
	if t.CreatedAt.Valid {
		resp.CreatedAt = t.CreatedAt.Time.Format(time.RFC3339)
	}
	return resp
}

func memberInfoToResponse(m service.MemberInfo) memberResponse {
	return memberResponse{
		UserID:   m.UserID,
		Name:     m.Name,
		Email:    m.Email,
		Role:     m.Role,
		JoinedAt: m.JoinedAt.Format(time.RFC3339),
	}
}

// requireTeamAccess is an inline check used by every team-scoped handler:
// the JWT team_id must match the URL {id} before any DB call is made.
// Returns false and writes 403 if they don't match.
func requireTeamAccess(w http.ResponseWriter, r *http.Request, ac auth.AuthContext) (string, bool) {
	teamID := chi.URLParam(r, "id")
	if ac.TeamID != teamID {
		writeError(w, http.StatusForbidden, "forbidden", "JWT team does not match requested team; use switch-team first")
		return "", false
	}
	return teamID, true
}

// List handles GET /v1/teams
// Returns all teams the authenticated user belongs to.
func (h *teamHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	teams, err := h.svc.ListTeamsForUser(r.Context(), ac.UserID)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	resp := make([]teamWithRoleResponse, len(teams))
	for i, t := range teams {
		resp[i] = teamWithRoleResponse{
			teamResponse: teamToResponse(t.Team),
			Role:         t.Role,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// Create handles POST /v1/teams
// Creates a new team owned by the authenticated user.
func (h *teamHandler) Create(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)

	team, err := h.svc.CreateTeam(r.Context(), ac.UserID, req.Name)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusCreated, teamWithRoleResponse{
		teamResponse: teamToResponse(team.Team),
		Role:         team.Role,
	})
}

// Get handles GET /v1/teams/{id}
// Returns team info and member list.
func (h *teamHandler) Get(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	teamID, ok := requireTeamAccess(w, r, ac)
	if !ok {
		return
	}

	team, err := h.svc.GetTeam(r.Context(), teamID)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	members, err := h.svc.GetMembers(r.Context(), teamID)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	memberResp := make([]memberResponse, len(members))
	for i, m := range members {
		memberResp[i] = memberInfoToResponse(m)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"team":    teamToResponse(team),
		"members": memberResp,
	})
}

// Rename handles PATCH /v1/teams/{id}
// Renames the team. Requires admin or owner role (verified from DB).
func (h *teamHandler) Rename(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	teamID, ok := requireTeamAccess(w, r, ac)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)

	if err := h.svc.RenameTeam(r.Context(), teamID, ac.UserID, req.Name); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Delete handles DELETE /v1/teams/{id}
// Soft-deletes the team and destroys active sandboxes. Owner only.
func (h *teamHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	teamID, ok := requireTeamAccess(w, r, ac)
	if !ok {
		return
	}

	if err := h.svc.DeleteTeam(r.Context(), teamID, ac.UserID); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListMembers handles GET /v1/teams/{id}/members
func (h *teamHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	teamID, ok := requireTeamAccess(w, r, ac)
	if !ok {
		return
	}

	members, err := h.svc.GetMembers(r.Context(), teamID)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	resp := make([]memberResponse, len(members))
	for i, m := range members {
		resp[i] = memberInfoToResponse(m)
	}
	writeJSON(w, http.StatusOK, resp)
}

// AddMember handles POST /v1/teams/{id}/members
// Adds a user by email. Requires admin or owner (verified from DB).
func (h *teamHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	teamID, ok := requireTeamAccess(w, r, ac)
	if !ok {
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "email is required")
		return
	}

	member, err := h.svc.AddMember(r.Context(), teamID, ac.UserID, req.Email)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusCreated, memberInfoToResponse(member))
}

// RemoveMember handles DELETE /v1/teams/{id}/members/{uid}
// Removes a member. Requires admin or owner (verified from DB). Owner cannot be removed.
func (h *teamHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	teamID, ok := requireTeamAccess(w, r, ac)
	if !ok {
		return
	}
	targetUserID := chi.URLParam(r, "uid")

	if err := h.svc.RemoveMember(r.Context(), teamID, ac.UserID, targetUserID); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UpdateMemberRole handles PATCH /v1/teams/{id}/members/{uid}
// Changes a member's role (admin or member). Owner's role cannot be changed.
func (h *teamHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	teamID, ok := requireTeamAccess(w, r, ac)
	if !ok {
		return
	}
	targetUserID := chi.URLParam(r, "uid")

	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if err := h.svc.UpdateMemberRole(r.Context(), teamID, ac.UserID, targetUserID, req.Role); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Leave handles POST /v1/teams/{id}/leave
// Removes the calling user from the team. Owner cannot leave.
func (h *teamHandler) Leave(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	teamID, ok := requireTeamAccess(w, r, ac)
	if !ok {
		return
	}

	if err := h.svc.LeaveTeam(r.Context(), teamID, ac.UserID); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
