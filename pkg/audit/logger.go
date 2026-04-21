package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/events"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// AuditLogger writes audit log entries for user-initiated and system events.
// All methods are fire-and-forget: failures are logged via slog and never
// propagated to the caller.
type AuditLogger struct {
	db  *db.Queries
	pub events.EventPublisher // optional — nil disables event publishing
}

// New constructs an AuditLogger without event publishing.
func New(queries *db.Queries) *AuditLogger {
	return &AuditLogger{db: queries}
}

// NewWithPublisher constructs an AuditLogger that also publishes channel events.
func NewWithPublisher(queries *db.Queries, pub events.EventPublisher) *AuditLogger {
	return &AuditLogger{db: queries, pub: pub}
}

// publish sends an event to the notification stream if a publisher is configured.
func (l *AuditLogger) publish(ctx context.Context, e events.Event) {
	if l.pub != nil {
		l.pub.Publish(ctx, e)
	}
}

// actorToEvent converts auth context fields to an events.Actor.
func actorToEvent(ac auth.AuthContext) events.Actor {
	at, aid, aname := actorFields(ac)
	return events.Actor{Type: events.ActorKind(at), ID: aid, Name: aname}
}

// systemActor returns an events.Actor for system-initiated events.
func systemActor() events.Actor {
	return events.Actor{Type: events.ActorSystem}
}

// actorFields extracts actor_type, actor_id, and actor_name from an AuthContext.
// actor_id is stored as a prefixed string in the TEXT column.
func actorFields(ac auth.AuthContext) (actorType, actorID, actorName string) {
	if ac.UserID.Valid {
		return "user", id.FormatUserID(ac.UserID), ac.Name
	}
	if ac.APIKeyID.Valid {
		return "api_key", id.FormatAPIKeyID(ac.APIKeyID), ac.APIKeyName
	}
	return "system", "", ""
}

func (l *AuditLogger) write(ctx context.Context, p db.InsertAuditLogParams) {
	if err := l.db.InsertAuditLog(ctx, p); err != nil {
		slog.Warn("audit: failed to write log entry",
			"action", p.Action,
			"resource_type", p.ResourceType,
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

// optText returns a valid pgtype.Text if s is non-empty, otherwise an invalid (NULL) one.
func optText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// --- Sandbox events (scope: team) ---

func (l *AuditLogger) LogSandboxCreate(ctx context.Context, ac auth.AuthContext, sandboxID pgtype.UUID, template string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "sandbox",
		ResourceID:   optText(id.FormatSandboxID(sandboxID)),
		Action:       "create",
		Scope:        "team",
		Status:       "success",
		Metadata:     marshalMeta(map[string]any{"template": template}),
	})
	l.publish(ctx, events.Event{
		Event:     events.CapsuleCreated,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
	})
}

func (l *AuditLogger) LogSandboxPause(ctx context.Context, ac auth.AuthContext, sandboxID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "sandbox",
		ResourceID:   optText(id.FormatSandboxID(sandboxID)),
		Action:       "pause",
		Scope:        "team",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
	l.publish(ctx, events.Event{
		Event:     events.CapsulePaused,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
	})
}

// LogSandboxAutoPause records a system-initiated auto-pause (TTL or host reconciler).
func (l *AuditLogger) LogSandboxAutoPause(ctx context.Context, teamID, sandboxID pgtype.UUID) {
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       teamID,
		ActorType:    "system",
		ActorID:      pgtype.Text{},
		ActorName:    "",
		ResourceType: "sandbox",
		ResourceID:   optText(id.FormatSandboxID(sandboxID)),
		Action:       "pause",
		Scope:        "team",
		Status:       "info",
		Metadata:     []byte("{}"),
	})
	l.publish(ctx, events.Event{
		Event:     events.CapsulePaused,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     systemActor(),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
	})
}

func (l *AuditLogger) LogSandboxResume(ctx context.Context, ac auth.AuthContext, sandboxID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "sandbox",
		ResourceID:   optText(id.FormatSandboxID(sandboxID)),
		Action:       "resume",
		Scope:        "team",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
	l.publish(ctx, events.Event{
		Event:     events.CapsuleRunning,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
	})
}

func (l *AuditLogger) LogSandboxDestroy(ctx context.Context, ac auth.AuthContext, sandboxID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "sandbox",
		ResourceID:   optText(id.FormatSandboxID(sandboxID)),
		Action:       "destroy",
		Scope:        "team",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
	l.publish(ctx, events.Event{
		Event:     events.CapsuleDestroyed,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
	})
}

// --- Snapshot events (scope: team) ---

func (l *AuditLogger) LogSnapshotCreate(ctx context.Context, ac auth.AuthContext, name string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "snapshot",
		ResourceID:   optText(name),
		Action:       "create",
		Scope:        "team",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
	l.publish(ctx, events.Event{
		Event:     events.SnapshotCreated,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: name, Type: "snapshot"},
	})
}

func (l *AuditLogger) LogSnapshotDelete(ctx context.Context, ac auth.AuthContext, name string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "snapshot",
		ResourceID:   optText(name),
		Action:       "delete",
		Scope:        "team",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
	l.publish(ctx, events.Event{
		Event:     events.SnapshotDeleted,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: name, Type: "snapshot"},
	})
}

// --- Team events (scope: team) ---

func (l *AuditLogger) LogTeamRename(ctx context.Context, ac auth.AuthContext, teamID pgtype.UUID, oldName, newName string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "team",
		ResourceID:   optText(id.FormatTeamID(teamID)),
		Action:       "rename",
		Scope:        "team",
		Status:       "info",
		Metadata:     marshalMeta(map[string]any{"old_name": oldName, "new_name": newName}),
	})
}

// --- Channel events (scope: team) ---

func (l *AuditLogger) LogChannelCreate(ctx context.Context, ac auth.AuthContext, channelID pgtype.UUID, name, provider string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "channel",
		ResourceID:   optText(id.FormatChannelID(channelID)),
		Action:       "create",
		Scope:        "team",
		Status:       "success",
		Metadata:     marshalMeta(map[string]any{"name": name, "provider": provider}),
	})
}

func (l *AuditLogger) LogChannelUpdate(ctx context.Context, ac auth.AuthContext, channelID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "channel",
		ResourceID:   optText(id.FormatChannelID(channelID)),
		Action:       "update",
		Scope:        "team",
		Status:       "info",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogChannelRotateConfig(ctx context.Context, ac auth.AuthContext, channelID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "channel",
		ResourceID:   optText(id.FormatChannelID(channelID)),
		Action:       "rotate_config",
		Scope:        "team",
		Status:       "info",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogChannelDelete(ctx context.Context, ac auth.AuthContext, channelID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "channel",
		ResourceID:   optText(id.FormatChannelID(channelID)),
		Action:       "delete",
		Scope:        "team",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
}

// --- API key events (scope: team) ---

func (l *AuditLogger) LogAPIKeyCreate(ctx context.Context, ac auth.AuthContext, keyID pgtype.UUID, keyName string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "api_key",
		ResourceID:   optText(id.FormatAPIKeyID(keyID)),
		Action:       "create",
		Scope:        "team",
		Status:       "success",
		Metadata:     marshalMeta(map[string]any{"name": keyName}),
	})
}

func (l *AuditLogger) LogAPIKeyRevoke(ctx context.Context, ac auth.AuthContext, keyID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "api_key",
		ResourceID:   optText(id.FormatAPIKeyID(keyID)),
		Action:       "revoke",
		Scope:        "team",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
}

// --- Member events (scope: admin) ---

func (l *AuditLogger) LogMemberAdd(ctx context.Context, ac auth.AuthContext, targetUserID pgtype.UUID, targetEmail, role string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "member",
		ResourceID:   optText(id.FormatUserID(targetUserID)),
		Action:       "add",
		Scope:        "admin",
		Status:       "success",
		Metadata:     marshalMeta(map[string]any{"email": targetEmail, "role": role}),
	})
}

func (l *AuditLogger) LogMemberRemove(ctx context.Context, ac auth.AuthContext, targetUserID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "member",
		ResourceID:   optText(id.FormatUserID(targetUserID)),
		Action:       "remove",
		Scope:        "admin",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogMemberLeave(ctx context.Context, ac auth.AuthContext) {
	actorType, actorID, actorName := actorFields(ac)
	resourceID := ""
	if ac.UserID.Valid {
		resourceID = id.FormatUserID(ac.UserID)
	}
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "member",
		ResourceID:   optText(resourceID),
		Action:       "leave",
		Scope:        "admin",
		Status:       "info",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogMemberRoleUpdate(ctx context.Context, ac auth.AuthContext, targetUserID pgtype.UUID, newRole string) {
	actorType, actorID, actorName := actorFields(ac)
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       ac.TeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "member",
		ResourceID:   optText(id.FormatUserID(targetUserID)),
		Action:       "role_update",
		Scope:        "admin",
		Status:       "info",
		Metadata:     marshalMeta(map[string]any{"new_role": newRole}),
	})
}

// --- Host events (scope: admin) ---

func (l *AuditLogger) LogHostCreate(ctx context.Context, ac auth.AuthContext, hostID, teamID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	// BYOC hosts log to the owning team; shared hosts log to the platform team.
	logTeamID := teamID
	if !logTeamID.Valid {
		logTeamID = id.PlatformTeamID
	}
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       logTeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "host",
		ResourceID:   optText(id.FormatHostID(hostID)),
		Action:       "create",
		Scope:        "admin",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
}

func (l *AuditLogger) LogHostDelete(ctx context.Context, ac auth.AuthContext, hostID, teamID pgtype.UUID) {
	actorType, actorID, actorName := actorFields(ac)
	// BYOC hosts log to the owning team; shared hosts log to the platform team.
	logTeamID := teamID
	if !logTeamID.Valid {
		logTeamID = id.PlatformTeamID
	}
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       logTeamID,
		ActorType:    actorType,
		ActorID:      optText(actorID),
		ActorName:    actorName,
		ResourceType: "host",
		ResourceID:   optText(id.FormatHostID(hostID)),
		Action:       "delete",
		Scope:        "admin",
		Status:       "warning",
		Metadata:     []byte("{}"),
	})
}

// LogHostMarkedDown records a system-initiated host status transition to unreachable.
// Scoped to "team" so BYOC team members can see when their hosts go down.
func (l *AuditLogger) LogHostMarkedDown(ctx context.Context, teamID, hostID pgtype.UUID) {
	if !teamID.Valid {
		return
	}
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       teamID,
		ActorType:    "system",
		ActorID:      pgtype.Text{},
		ActorName:    "",
		ResourceType: "host",
		ResourceID:   optText(id.FormatHostID(hostID)),
		Action:       "marked_down",
		Scope:        "team",
		Status:       "error",
		Metadata:     []byte("{}"),
	})
	l.publish(ctx, events.Event{
		Event:     events.HostDown,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     systemActor(),
		Resource:  events.Resource{ID: id.FormatHostID(hostID), Type: "host"},
	})
}

// LogHostMarkedUp records a system-initiated host status transition back to online.
// Scoped to "team" so BYOC team members can see when their hosts recover.
func (l *AuditLogger) LogHostMarkedUp(ctx context.Context, teamID, hostID pgtype.UUID) {
	if !teamID.Valid {
		return
	}
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       teamID,
		ActorType:    "system",
		ActorID:      pgtype.Text{},
		ActorName:    "",
		ResourceType: "host",
		ResourceID:   optText(id.FormatHostID(hostID)),
		Action:       "marked_up",
		Scope:        "team",
		Status:       "success",
		Metadata:     []byte("{}"),
	})
	l.publish(ctx, events.Event{
		Event:     events.HostUp,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     systemActor(),
		Resource:  events.Resource{ID: id.FormatHostID(hostID), Type: "host"},
	})
}
