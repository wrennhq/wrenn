package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
)

// AuditLogger writes audit log entries for user-initiated and system events.
// All methods are fire-and-forget: failures are logged via slog and never
// propagated to the caller.
type AuditLogger struct {
	db *db.Queries
}

// New constructs an AuditLogger.
func New(queries *db.Queries) *AuditLogger {
	return &AuditLogger{db: queries}
}

// actorFields extracts actor_type, actor_id, and actor_name from an AuthContext.
func actorFields(ac auth.AuthContext) (actorType string, actorID pgtype.Text, actorName pgtype.Text) {
	if ac.UserID != "" {
		return "user",
			pgtype.Text{String: ac.UserID, Valid: true},
			pgtype.Text{String: ac.Name, Valid: ac.Name != ""}
	}
	if ac.APIKeyID != "" {
		return "api_key",
			pgtype.Text{String: ac.APIKeyID, Valid: true},
			pgtype.Text{String: ac.APIKeyName, Valid: true}
	}
	return "system", pgtype.Text{}, pgtype.Text{}
}

func (l *AuditLogger) write(ctx context.Context, p db.InsertAuditLogParams) {
	if err := l.db.InsertAuditLog(ctx, p); err != nil {
		slog.Warn("audit: failed to write log entry",
			"action", p.Action,
			"resource_type", p.ResourceType,
			"team_id", p.TeamID,
			"error", err,
		)
	}
}

func marshalMeta(meta map[string]any) []byte {
	if len(meta) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// --- Sandbox events (scope: team) ---

func (l *AuditLogger) LogSandboxCreate(ctx context.Context, ac auth.AuthContext, sandboxID, template string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "sandbox",
		ResourceID:   pgtype.Text{String: sandboxID, Valid: true},
		Action:       "create",
		Scope:        "team",
		Status:       "success",
		Metadata:     marshalMeta(map[string]any{"template": template}),
	})
}

func (l *AuditLogger) LogSandboxPause(ctx context.Context, ac auth.AuthContext, sandboxID string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "sandbox",
		ResourceID:   pgtype.Text{String: sandboxID, Valid: true},
		Action:       "pause",
		Scope:        "team",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
}

// LogSandboxAutoPause records a system-initiated auto-pause (TTL or host reconciler).
func (l *AuditLogger) LogSandboxAutoPause(ctx context.Context, teamID, sandboxID string) {
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       teamID,
		ActorType:    "system",
		ActorID:      pgtype.Text{},
		ActorName:    pgtype.Text{},
		ResourceType: "sandbox",
		ResourceID:   pgtype.Text{String: sandboxID, Valid: true},
		Action:       "pause",
		Scope:        "team",
		Status:       "info",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogSandboxResume(ctx context.Context, ac auth.AuthContext, sandboxID string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "sandbox",
		ResourceID:   pgtype.Text{String: sandboxID, Valid: true},
		Action:       "resume",
		Scope:        "team",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogSandboxDestroy(ctx context.Context, ac auth.AuthContext, sandboxID string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "sandbox",
		ResourceID:   pgtype.Text{String: sandboxID, Valid: true},
		Action:       "destroy",
		Scope:        "team",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
}

// --- Snapshot events (scope: team) ---

func (l *AuditLogger) LogSnapshotCreate(ctx context.Context, ac auth.AuthContext, name string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "snapshot",
		ResourceID:   pgtype.Text{String: name, Valid: true},
		Action:       "create",
		Scope:        "team",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogSnapshotDelete(ctx context.Context, ac auth.AuthContext, name string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "snapshot",
		ResourceID:   pgtype.Text{String: name, Valid: true},
		Action:       "delete",
		Scope:        "team",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
}

// --- Team events (scope: team) ---

func (l *AuditLogger) LogTeamRename(ctx context.Context, ac auth.AuthContext, teamID, oldName, newName string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "team",
		ResourceID:   pgtype.Text{String: teamID, Valid: true},
		Action:       "rename",
		Scope:        "team",
		Status:       "info",
		Metadata:     marshalMeta(map[string]any{"old_name": oldName, "new_name": newName}),
	})
}

// --- API key events (scope: team) ---

func (l *AuditLogger) LogAPIKeyCreate(ctx context.Context, ac auth.AuthContext, keyID, keyName string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "api_key",
		ResourceID:   pgtype.Text{String: keyID, Valid: true},
		Action:       "create",
		Scope:        "team",
		Status:       "success",
		Metadata:     marshalMeta(map[string]any{"name": keyName}),
	})
}

func (l *AuditLogger) LogAPIKeyRevoke(ctx context.Context, ac auth.AuthContext, keyID string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "api_key",
		ResourceID:   pgtype.Text{String: keyID, Valid: true},
		Action:       "revoke",
		Scope:        "team",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
}

// --- Member events (scope: admin) ---

func (l *AuditLogger) LogMemberAdd(ctx context.Context, ac auth.AuthContext, targetUserID, targetEmail, role string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "member",
		ResourceID:   pgtype.Text{String: targetUserID, Valid: true},
		Action:       "add",
		Scope:        "admin",
		Status:       "success",
		Metadata:     marshalMeta(map[string]any{"email": targetEmail, "role": role}),
	})
}

func (l *AuditLogger) LogMemberRemove(ctx context.Context, ac auth.AuthContext, targetUserID string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "member",
		ResourceID:   pgtype.Text{String: targetUserID, Valid: true},
		Action:       "remove",
		Scope:        "admin",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogMemberLeave(ctx context.Context, ac auth.AuthContext) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "member",
		ResourceID:   pgtype.Text{String: ac.UserID, Valid: ac.UserID != ""},
		Action:       "leave",
		Scope:        "admin",
		Status:       "info",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogMemberRoleUpdate(ctx context.Context, ac auth.AuthContext, targetUserID, newRole string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "member",
		ResourceID:   pgtype.Text{String: targetUserID, Valid: true},
		Action:       "role_update",
		Scope:        "admin",
		Status:       "info",
		Metadata:     marshalMeta(map[string]any{"new_role": newRole}),
	})
}

// --- Host events (scope: admin) ---

func (l *AuditLogger) LogHostCreate(ctx context.Context, ac auth.AuthContext, hostID, teamID string) {
	actorType, actorID, actorName := actorFields(ac)
	// For shared hosts with no owning team, use the caller's team.
	logTeamID := teamID
	if logTeamID == "" {
		logTeamID = ac.TeamID
	}
	if logTeamID == "" {
		return
	}
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       logTeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "host",
		ResourceID:   pgtype.Text{String: hostID, Valid: true},
		Action:       "create",
		Scope:        "admin",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogHostDelete(ctx context.Context, ac auth.AuthContext, hostID, teamID string) {
	actorType, actorID, actorName := actorFields(ac)
	logTeamID := teamID
	if logTeamID == "" {
		logTeamID = ac.TeamID
	}
	if logTeamID == "" {
		return
	}
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       logTeamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: "host",
		ResourceID:   pgtype.Text{String: hostID, Valid: true},
		Action:       "delete",
		Scope:        "admin",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
}

// LogHostMarkedDown records a system-initiated host status transition to unreachable.
// teamID must be non-empty (BYOC hosts only); shared hosts are not logged.
func (l *AuditLogger) LogHostMarkedDown(ctx context.Context, teamID, hostID string) {
	if teamID == "" {
		return
	}
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       teamID,
		ActorType:    "system",
		ActorID:      pgtype.Text{},
		ActorName:    pgtype.Text{},
		ResourceType: "host",
		ResourceID:   pgtype.Text{String: hostID, Valid: true},
		Action:       "marked_down",
		Scope:        "admin",
		Status:       "error",
		Metadata:     []byte("{}"),
	})
}

// LogHostMarkedUp records a system-initiated host status transition back to online.
// teamID must be non-empty (BYOC hosts only); shared hosts are not logged.
func (l *AuditLogger) LogHostMarkedUp(ctx context.Context, teamID, hostID string) {
	if teamID == "" {
		return
	}
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       teamID,
		ActorType:    "system",
		ActorID:      pgtype.Text{},
		ActorName:    pgtype.Text{},
		ResourceType: "host",
		ResourceID:   pgtype.Text{String: hostID, Valid: true},
		Action:       "marked_up",
		Scope:        "admin",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
}
