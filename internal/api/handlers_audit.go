package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

type auditHandler struct {
	svc *service.AuditService
}

func newAuditHandler(svc *service.AuditService) *auditHandler {
	return &auditHandler{svc: svc}
}

type auditLogResponse struct {
	ID           string         `json:"id"`
	ActorType    string         `json:"actor_type"`
	ActorID      string         `json:"actor_id,omitempty"`
	ActorName    string         `json:"actor_name,omitempty"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id,omitempty"`
	Action       string         `json:"action"`
	Scope        string         `json:"scope"`
	Status       string         `json:"status"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    string         `json:"created_at"`
}

// parseAuditParams extracts common query parameters for audit log listing.
func parseAuditParams(r *http.Request) (before time.Time, beforeID pgtype.UUID, limit int, err error) {
	limit = 50

	if s := r.URL.Query().Get("before"); s != "" {
		before, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return
		}
	}

	if s := r.URL.Query().Get("limit"); s != "" {
		n, parseErr := strconv.Atoi(s)
		if parseErr != nil || n < 1 {
			err = parseErr
			return
		}
		limit = n
	}

	if s := r.URL.Query().Get("before_id"); s != "" {
		beforeID, err = id.ParseAuditLogID(s)
		if err != nil {
			return
		}
	}

	return
}

// writeAuditResponse serializes audit entries into a paginated JSON response.
func writeAuditResponse(w http.ResponseWriter, entries []service.AuditEntry) {
	items := make([]auditLogResponse, len(entries))
	for i, e := range entries {
		items[i] = auditLogResponse{
			ID:           e.ID,
			ActorType:    e.ActorType,
			ActorID:      e.ActorID,
			ActorName:    e.ActorName,
			ResourceType: e.ResourceType,
			ResourceID:   e.ResourceID,
			Action:       e.Action,
			Scope:        e.Scope,
			Status:       e.Status,
			Metadata:     e.Metadata,
			CreatedAt:    e.CreatedAt.UTC().Format(time.RFC3339),
		}
	}

	resp := map[string]any{"items": items}
	if len(items) > 0 {
		last := entries[len(entries)-1]
		resp["next_before"] = last.CreatedAt.UTC().Format(time.RFC3339)
		resp["next_before_id"] = last.ID
	}

	writeJSON(w, http.StatusOK, resp)
}

// List handles GET /v1/audit-logs.
// Query params:
//   - before: RFC3339 timestamp cursor (exclusive); omit to start from latest
//   - limit:  page size, default 50, max 200
//   - resource_type: filter by resource type (sandbox, snapshot, team, api_key, member, host)
//   - action: filter by action verb
//
// Members see only team-scoped events; admins/owners see all.
func (h *auditHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	before, beforeID, limit, err := parseAuditParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid query parameters")
		return
	}

	entries, err := h.svc.List(r.Context(), service.AuditListParams{
		TeamID:        ac.TeamID,
		AdminScoped:   ac.Role == "owner" || ac.Role == "admin",
		ResourceTypes: parseMultiParam(r.URL.Query()["resource_type"]),
		Actions:       parseMultiParam(r.URL.Query()["action"]),
		Before:        before,
		BeforeID:      beforeID,
		Limit:         limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list audit logs")
		return
	}

	writeAuditResponse(w, entries)
}

// AdminList handles GET /v1/admin/audit-logs.
// Returns audit logs for the platform team (team 0) with both team and admin scopes.
// Uses the same query params as List.
func (h *auditHandler) AdminList(w http.ResponseWriter, r *http.Request) {
	before, beforeID, limit, err := parseAuditParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid query parameters")
		return
	}

	entries, err := h.svc.List(r.Context(), service.AuditListParams{
		TeamID:        id.PlatformTeamID,
		AdminScoped:   true,
		ResourceTypes: parseMultiParam(r.URL.Query()["resource_type"]),
		Actions:       parseMultiParam(r.URL.Query()["action"]),
		Before:        before,
		BeforeID:      beforeID,
		Limit:         limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list audit logs")
		return
	}

	writeAuditResponse(w, entries)
}

// parseMultiParam flattens repeated params and comma-separated values into a
// single deduplicated slice. Empty strings are dropped. Returns nil (no filter)
// when no values are present.
//
// Both ?resource_type=sandbox&resource_type=snapshot
// and  ?resource_type=sandbox,snapshot are accepted.
func parseMultiParam(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := seen[part]; !ok {
				seen[part] = struct{}{}
				out = append(out, part)
			}
		}
	}
	return out
}
