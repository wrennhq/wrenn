package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/db"
)

const auditMaxLimit = 200

// AuditEntry is a single audit log record returned by List.
type AuditEntry struct {
	ID           string
	TeamID       string
	ActorType    string
	ActorID      string // empty for system
	ActorName    string // empty for system
	ResourceType string
	ResourceID   string // empty when not applicable
	Action       string
	Scope        string
	Status       string // 'success', 'info', 'warning', 'error'
	Metadata     map[string]any
	CreatedAt    time.Time
}

// AuditListParams controls the ListAuditLogs query.
type AuditListParams struct {
	TeamID        string
	AdminScoped   bool      // true → include admin-scoped events; false → team-scoped only
	ResourceTypes []string  // empty = no filter; multiple values = OR match
	Actions       []string  // empty = no filter; multiple values = OR match
	Before        time.Time // zero = no cursor (start from latest)
	BeforeID      string    // tie-breaker: id of the last item at the Before timestamp; empty = no tie-break
	Limit         int       // clamped to auditMaxLimit by the handler
}

// AuditService provides the read side of the audit log.
type AuditService struct {
	DB *db.Queries
}

// List returns a page of audit log entries for the given team.
func (s *AuditService) List(ctx context.Context, p AuditListParams) ([]AuditEntry, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > auditMaxLimit {
		limit = auditMaxLimit
	}

	scopes := []string{"team"}
	if p.AdminScoped {
		scopes = append(scopes, "admin")
	}

	var before pgtype.Timestamptz
	if !p.Before.IsZero() {
		before = pgtype.Timestamptz{Time: p.Before, Valid: true}
	}

	resourceTypes := p.ResourceTypes
	if resourceTypes == nil {
		resourceTypes = []string{}
	}
	actions := p.Actions
	if actions == nil {
		actions = []string{}
	}

	rows, err := s.DB.ListAuditLogs(ctx, db.ListAuditLogsParams{
		TeamID:  p.TeamID,
		Column2: scopes,
		Column3: resourceTypes,
		Column4: actions,
		Column5: before,
		ID:      p.BeforeID,
		Limit:   int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}

	entries := make([]AuditEntry, len(rows))
	for i, row := range rows {
		var meta map[string]any
		if len(row.Metadata) > 0 {
			_ = json.Unmarshal(row.Metadata, &meta)
		}
		entries[i] = AuditEntry{
			ID:           row.ID,
			TeamID:       row.TeamID,
			ActorType:    row.ActorType,
			ActorID:      row.ActorID.String,
			ActorName:    row.ActorName.String,
			ResourceType: row.ResourceType,
			ResourceID:   row.ResourceID.String,
			Action:       row.Action,
			Scope:        row.Scope,
			Status:       row.Status,
			Metadata:     meta,
			CreatedAt:    row.CreatedAt.Time,
		}
	}
	return entries, nil
}
