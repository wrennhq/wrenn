package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/audit"
	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
	"git.omukk.dev/wrenn/wrenn/internal/service"
)

type teamHandler struct {
	svc   *service.TeamService
	audit *audit.AuditLogger
}

func newTeamHandler(svc *service.TeamService, al *audit.AuditLogger) *teamHandler {
	return &teamHandler{svc: svc, audit: al}
}

// teamResponse is the JSON shape for a team.
type teamResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	IsByoc    bool   `json:"is_byoc"`
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
		ID:     id.FormatTeamID(t.ID),
		Name:   t.Name,
		Slug:   t.Slug,
		IsByoc: t.IsByoc,
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
func requireTeamAccess(w http.ResponseWriter, r *http.Request, ac auth.AuthContext) (pgtype.UUID, bool) {
	teamIDStr := chi.URLParam(r, "id")
	teamID, err := id.ParseTeamID(teamIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid team ID")
		return pgtype.UUID{}, false
	}
	if ac.TeamID != teamID {
		writeError(w, http.StatusForbidden, "forbidden", "JWT team does not match requested team; use switch-team first")
		return pgtype.UUID{}, false
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

	// Fetch old name for audit log before renaming.
	oldTeam, err := h.svc.GetTeam(r.Context(), teamID)
	if err != nil {
		slog.Warn("audit: could not fetch old team name for rename log", "team_id", id.FormatTeamID(teamID), "error", err)
	}

	if err := h.svc.RenameTeam(r.Context(), teamID, ac.UserID, req.Name); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogTeamRename(r.Context(), ac, teamID, oldTeam.Name, req.Name)
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

	// member.UserID is already formatted with prefix; parse it back for the audit logger.
	targetUserID, parseErr := id.ParseUserID(member.UserID)
	if parseErr == nil {
		h.audit.LogMemberAdd(r.Context(), ac, targetUserID, member.Email, member.Role)
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
	targetUserIDStr := chi.URLParam(r, "uid")

	targetUserID, err := id.ParseUserID(targetUserIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid user ID")
		return
	}

	if err := h.svc.RemoveMember(r.Context(), teamID, ac.UserID, targetUserID); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogMemberRemove(r.Context(), ac, targetUserID)
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
	targetUserIDStr := chi.URLParam(r, "uid")

	targetUserID, err := id.ParseUserID(targetUserIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid user ID")
		return
	}

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

	h.audit.LogMemberRoleUpdate(r.Context(), ac, targetUserID, req.Role)
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

	h.audit.LogMemberLeave(r.Context(), ac)
	w.WriteHeader(http.StatusNoContent)
}

// SetBYOC handles PUT /v1/admin/teams/{id}/byoc (admin only).
// Enables or disables the BYOC feature flag for a team.
func (h *teamHandler) SetBYOC(w http.ResponseWriter, r *http.Request) {
	teamIDStr := chi.URLParam(r, "id")

	teamID, err := id.ParseTeamID(teamIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid team ID")
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if err := h.svc.SetBYOC(r.Context(), teamID, req.Enabled); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AdminListTeams handles GET /v1/admin/teams?page=1
// Returns a paginated list of all teams with member counts, owner info, and active sandbox counts.
func (h *teamHandler) AdminListTeams(w http.ResponseWriter, r *http.Request) {
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if _, err := fmt.Sscanf(p, "%d", &page); err != nil || page < 1 {
			page = 1
		}
	}
	const perPage = 100
	offset := int32((page - 1) * perPage)

	teams, total, err := h.svc.AdminListTeams(r.Context(), perPage, offset)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	type adminTeamResponse struct {
		ID                 string  `json:"id"`
		Name               string  `json:"name"`
		Slug               string  `json:"slug"`
		IsByoc             bool    `json:"is_byoc"`
		CreatedAt          string  `json:"created_at"`
		DeletedAt          *string `json:"deleted_at"`
		MemberCount        int32   `json:"member_count"`
		OwnerName          string  `json:"owner_name"`
		OwnerEmail         string  `json:"owner_email"`
		ActiveSandboxCount int32   `json:"active_sandbox_count"`
		ChannelCount       int32   `json:"channel_count"`
	}

	resp := make([]adminTeamResponse, len(teams))
	for i, t := range teams {
		r := adminTeamResponse{
			ID:                 id.FormatTeamID(t.ID),
			Name:               t.Name,
			Slug:               t.Slug,
			IsByoc:             t.IsByoc,
			CreatedAt:          t.CreatedAt.Format(time.RFC3339),
			MemberCount:        t.MemberCount,
			OwnerName:          t.OwnerName,
			OwnerEmail:         t.OwnerEmail,
			ActiveSandboxCount: t.ActiveSandboxCount,
			ChannelCount:       t.ChannelCount,
		}
		if t.DeletedAt != nil {
			s := t.DeletedAt.Format(time.RFC3339)
			r.DeletedAt = &s
		}
		resp[i] = r
	}

	totalPages := (total + perPage - 1) / perPage
	writeJSON(w, http.StatusOK, map[string]any{
		"teams":       resp,
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": totalPages,
	})
}

// AdminDeleteTeam handles DELETE /v1/admin/teams/{id}
// Soft-deletes a team and destroys all its active sandboxes.
func (h *teamHandler) AdminDeleteTeam(w http.ResponseWriter, r *http.Request) {
	teamIDStr := chi.URLParam(r, "id")

	teamID, err := id.ParseTeamID(teamIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid team ID")
		return
	}

	if err := h.svc.AdminDeleteTeam(r.Context(), teamID); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
