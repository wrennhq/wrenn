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

// publishTransient mirrors an event on the SSE Pub/Sub channel only.
func (l *AuditLogger) publishTransient(ctx context.Context, e events.Event) {
	if l.pub != nil {
		l.pub.PublishTransient(ctx, e)
	}
}

// outcomeFromErr returns OutcomeSuccess when err is nil, OutcomeError otherwise.
func outcomeFromErr(err error) events.Outcome {
	if err != nil {
		return events.OutcomeError
	}
	return events.OutcomeSuccess
}

// auditStatusFor maps an error and success-status into the audit row status.
// On error → "error"; otherwise the supplied success status (e.g. "success", "warning", "info").
func auditStatusFor(err error, okStatus string) string {
	if err != nil {
		return "error"
	}
	return okStatus
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// mergeMeta returns a new map with err added when non-nil, preserving caller fields.
func mergeMeta(base map[string]any, err error) map[string]any {
	if err == nil {
		return base
	}
	out := make(map[string]any, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["error"] = err.Error()
	return out
}

// actorToEvent converts auth context fields to an events.Actor.
func actorToEvent(ac auth.AuthContext) events.Actor {
	at, aid, aname := actorFields(ac)
	return events.Actor{Type: events.ActorKind(at), ID: aid, Name: aname}
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

// Entry describes a single audit log event. Extensions (e.g. the cloud repo)
// use this with AuditLogger.Log to record custom events without modifying the
// OSS typed methods.
type Entry struct {
	TeamID       pgtype.UUID
	ActorType    string // "user", "api_key", "system"
	ActorID      string // prefixed ID string; empty for system
	ActorName    string // human-readable; empty for system
	ResourceType string
	ResourceID   string // prefixed ID or name; empty when not applicable
	Action       string
	Scope        string // "team" or "admin"
	Status       string // "success", "info", "warning", "error"
	Metadata     map[string]any
}

// Log writes a custom audit log entry. This is the extension point for the
// cloud repo to record events with resource types and actions not covered by
// the typed helpers (LogSandboxCreate, etc.). Fire-and-forget like all other
// audit methods.
func (l *AuditLogger) Log(ctx context.Context, e Entry) {
	l.write(ctx, db.InsertAuditLogParams{
		ID:           id.NewAuditLogID(),
		TeamID:       e.TeamID,
		ActorType:    e.ActorType,
		ActorID:      optText(e.ActorID),
		ActorName:    e.ActorName,
		ResourceType: e.ResourceType,
		ResourceID:   optText(e.ResourceID),
		Action:       e.Action,
		Scope:        e.Scope,
		Status:       e.Status,
		Metadata:     MarshalMeta(e.Metadata),
	})
}

// ActorFromContext extracts actor fields from an auth.AuthContext for use in
// custom audit entries. Returns actor_type, actor_id, and actor_name.
func ActorFromContext(ac auth.AuthContext) (actorType, actorID, actorName string) {
	return actorFields(ac)
}

// MarshalMeta serializes metadata to JSON bytes. Returns "{}" for nil/empty maps.
func MarshalMeta(meta map[string]any) []byte {
	return marshalMeta(meta)
}

// optText returns a valid pgtype.Text if s is non-empty, otherwise an invalid (NULL) one.
func optText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// --- Entry builders ---

// newEntry builds an Entry from an auth context with explicit team and scope.
func newEntry(ac auth.AuthContext, teamID pgtype.UUID, scope, resourceType, resourceID, action, status string, meta map[string]any) Entry {
	actorType, actorID, actorName := actorFields(ac)
	return Entry{
		TeamID:       teamID,
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Action:       action,
		Scope:        scope,
		Status:       status,
		Metadata:     meta,
	}
}

// newAdminEntry builds an Entry for platform-level admin actions (PlatformTeamID, scope "admin").
func newAdminEntry(ac auth.AuthContext, resourceType, resourceID, action, status string, meta map[string]any) Entry {
	return newEntry(ac, id.PlatformTeamID, "admin", resourceType, resourceID, action, status, meta)
}

// resolveHostTeamID returns the owning team for BYOC hosts, or PlatformTeamID for shared hosts.
func resolveHostTeamID(teamID pgtype.UUID) pgtype.UUID {
	if teamID.Valid {
		return teamID
	}
	return id.PlatformTeamID
}

// --- Sandbox events (scope: team) ---

// LogSandboxCreate records the result of a first-boot sandbox creation. err
// nil ⇒ success; non-nil ⇒ error. Writes audit row and publishes a
// capsule.create event with the derived outcome.
func (l *AuditLogger) LogSandboxCreate(ctx context.Context, ac auth.AuthContext, sandboxID pgtype.UUID, template string, err error) {
	meta := map[string]any{"template": template}
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "sandbox", id.FormatSandboxID(sandboxID), "create", auditStatusFor(err, "success"), mergeMeta(meta, err)))
	l.publish(ctx, events.Event{
		Event:     events.CapsuleCreate,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
		Metadata:  map[string]string{"template": template},
		Error:     errString(err),
	})
}

// LogSandboxPause records a user-initiated pause.
func (l *AuditLogger) LogSandboxPause(ctx context.Context, ac auth.AuthContext, sandboxID pgtype.UUID, err error) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "sandbox", id.FormatSandboxID(sandboxID), "pause", auditStatusFor(err, "success"), mergeMeta(nil, err)))
	l.publish(ctx, events.Event{
		Event:     events.CapsulePause,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
		Error:     errString(err),
	})
}

// LogSandboxAutoPause records a system-initiated auto-pause (TTL reaper or
// reconciler restoration of paused state). Always system actor; metadata
// carries the reason (e.g. "ttl_expired", "restored_after_host_recovery").
func (l *AuditLogger) LogSandboxAutoPause(ctx context.Context, teamID, sandboxID pgtype.UUID, reason string, err error) {
	meta := map[string]any{"reason": reason}
	l.Log(ctx, Entry{
		TeamID: teamID, ActorType: "system",
		ResourceType: "sandbox", ResourceID: id.FormatSandboxID(sandboxID),
		Action: "pause", Scope: "team", Status: auditStatusFor(err, "info"),
		Metadata: mergeMeta(meta, err),
	})
	l.publish(ctx, events.Event{
		Event:     events.CapsulePause,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     events.SystemActor(),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
		Metadata:  map[string]string{"reason": reason},
		Error:     errString(err),
	})
}

// LogSandboxResume records a user-initiated unpause (resume from paused state).
func (l *AuditLogger) LogSandboxResume(ctx context.Context, ac auth.AuthContext, sandboxID pgtype.UUID, err error) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "sandbox", id.FormatSandboxID(sandboxID), "resume", auditStatusFor(err, "success"), mergeMeta(nil, err)))
	l.publish(ctx, events.Event{
		Event:     events.CapsuleResume,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
		Error:     errString(err),
	})
}

// LogSandboxDestroy records a destroy action. ac carries the actor (user / api_key / system).
// reason is added to metadata when non-empty (e.g. "orphaned", "cleanup_after_create_error", "ttl_expired").
func (l *AuditLogger) LogSandboxDestroy(ctx context.Context, ac auth.AuthContext, sandboxID pgtype.UUID, err error) {
	l.LogSandboxDestroyWithReason(ctx, ac, sandboxID, "", err)
}

// LogSandboxDestroyWithReason is LogSandboxDestroy with an explicit reason.
func (l *AuditLogger) LogSandboxDestroyWithReason(ctx context.Context, ac auth.AuthContext, sandboxID pgtype.UUID, reason string, err error) {
	var (
		auditMeta map[string]any
		evtMeta   map[string]string
	)
	if reason != "" {
		auditMeta = map[string]any{"reason": reason}
		evtMeta = map[string]string{"reason": reason}
	}
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "sandbox", id.FormatSandboxID(sandboxID), "destroy", auditStatusFor(err, "warning"), mergeMeta(auditMeta, err)))
	l.publish(ctx, events.Event{
		Event:     events.CapsuleDestroy,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
		Metadata:  evtMeta,
		Error:     errString(err),
	})
}

// LogSandboxCreateSystem records a system-derived create failure (e.g. the
// reconciler inferring a failed first-boot after the grace period expired).
// It writes an "error" action — not a "create" one — so the entry reads as a
// capsule error rather than a system-attributed creation; the user-attributed
// create row is written separately by the request handler. reason and the
// "create" phase are added to metadata; err controls outcome.
func (l *AuditLogger) LogSandboxCreateSystem(ctx context.Context, teamID, sandboxID pgtype.UUID, reason string, err error) {
	meta := map[string]any{"reason": reason, "phase": "create"}
	l.Log(ctx, Entry{
		TeamID: teamID, ActorType: "system",
		ResourceType: "sandbox", ResourceID: id.FormatSandboxID(sandboxID),
		Action: "error", Scope: "team", Status: auditStatusFor(err, "info"),
		Metadata: mergeMeta(meta, err),
	})
	l.publish(ctx, events.Event{
		Event:     events.CapsuleCreate,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     events.SystemActor(),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
		Metadata:  map[string]string{"reason": reason},
		Error:     errString(err),
	})
}

// LogSandboxResumeSystem records a system-derived resume failure (typically
// reconciler-inferred error after the grace period). Like LogSandboxCreateSystem
// it writes an "error" action so the entry reads as a capsule error rather than
// a system-attributed resume; reason and the "resume" phase go to metadata.
func (l *AuditLogger) LogSandboxResumeSystem(ctx context.Context, teamID, sandboxID pgtype.UUID, reason string, err error) {
	meta := map[string]any{"reason": reason, "phase": "resume"}
	l.Log(ctx, Entry{
		TeamID: teamID, ActorType: "system",
		ResourceType: "sandbox", ResourceID: id.FormatSandboxID(sandboxID),
		Action: "error", Scope: "team", Status: auditStatusFor(err, "info"),
		Metadata: mergeMeta(meta, err),
	})
	l.publish(ctx, events.Event{
		Event:     events.CapsuleResume,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     events.SystemActor(),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
		Metadata:  map[string]string{"reason": reason},
		Error:     errString(err),
	})
}

// LogSandboxDestroySystem records a system-initiated destroy (orphan cleanup,
// cleanup-on-error, reconciler grace-period expiry). Always system actor.
func (l *AuditLogger) LogSandboxDestroySystem(ctx context.Context, teamID, sandboxID pgtype.UUID, reason string, err error) {
	meta := map[string]any{"reason": reason}
	l.Log(ctx, Entry{
		TeamID: teamID, ActorType: "system",
		ResourceType: "sandbox", ResourceID: id.FormatSandboxID(sandboxID),
		Action: "destroy", Scope: "team", Status: auditStatusFor(err, "warning"),
		Metadata: mergeMeta(meta, err),
	})
	l.publish(ctx, events.Event{
		Event:     events.CapsuleDestroy,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     events.SystemActor(),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
		Metadata:  map[string]string{"reason": reason},
		Error:     errString(err),
	})
}

// LogSandboxStateChanged is a transient (SSE-only) event for ephemeral status
// transitions (e.g. running → pausing → paused). Writes no audit row.
func (l *AuditLogger) LogSandboxStateChanged(ctx context.Context, teamID, sandboxID pgtype.UUID, from, to string) {
	l.publishTransient(ctx, events.Event{
		Event:     events.CapsuleStateChanged,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     events.SystemActor(),
		Resource:  events.Resource{ID: id.FormatSandboxID(sandboxID), Type: "sandbox"},
		Metadata:  map[string]string{"from": from, "to": to},
	})
}

// --- Snapshot events (scope: team) ---

// LogSnapshotCreateRequested records that a user requested an async snapshot.
// It writes the user-attributed audit row only — the terminal success/failure
// event is published later by the background goroutine (system actor). Mirrors
// the accept-time audit pattern used by LogSandboxPause.
func (l *AuditLogger) LogSnapshotCreateRequested(ctx context.Context, ac auth.AuthContext, name string) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "snapshot", name, "create", "success", nil))
}

// LogSnapshotCreateSystem records a system-actor snapshot transition inferred
// by a reconciler (e.g. the HostMonitor recovering or failing a sandbox stuck
// in "snapshotting"). It writes an audit row only and does NOT publish a
// SnapshotCreate event: the reconciler has no template name, and emitting one
// would surface a spurious "snapshot captured/failed" toast.
func (l *AuditLogger) LogSnapshotCreateSystem(ctx context.Context, teamID, sandboxID pgtype.UUID, reason string, err error) {
	l.Log(ctx, Entry{
		TeamID: teamID, ActorType: "system",
		ResourceType: "sandbox", ResourceID: id.FormatSandboxID(sandboxID),
		Action: "snapshot", Scope: "team", Status: auditStatusFor(err, "info"),
		Metadata: mergeMeta(map[string]any{"reason": reason}, err),
	})
}

func (l *AuditLogger) LogSnapshotDelete(ctx context.Context, ac auth.AuthContext, name string, err error) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "snapshot", name, "delete", auditStatusFor(err, "warning"), mergeMeta(nil, err)))
	l.publish(ctx, events.Event{
		Event:     events.SnapshotDelete,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: name, Type: "snapshot"},
		Error:     errString(err),
	})
}

// LogSnapshotDeleteSystem records system-initiated snapshot cleanup
// (e.g. rollback after a failed snapshot create). Always system actor.
func (l *AuditLogger) LogSnapshotDeleteSystem(ctx context.Context, teamID pgtype.UUID, name, reason string, err error) {
	meta := map[string]any{"reason": reason}
	l.Log(ctx, Entry{
		TeamID: teamID, ActorType: "system",
		ResourceType: "snapshot", ResourceID: name,
		Action: "delete", Scope: "team", Status: auditStatusFor(err, "warning"),
		Metadata: mergeMeta(meta, err),
	})
	l.publish(ctx, events.Event{
		Event:     events.SnapshotDelete,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     events.SystemActor(),
		Resource:  events.Resource{ID: name, Type: "snapshot"},
		Metadata:  map[string]string{"reason": reason},
		Error:     errString(err),
	})
}

// --- Volume events (scope: team) ---

// LogVolumeCreate records the creation of an external storage volume.
func (l *AuditLogger) LogVolumeCreate(ctx context.Context, ac auth.AuthContext, volumeID pgtype.UUID, name string, sizeMB int, err error) {
	meta := map[string]any{"name": name, "size_mb": sizeMB}
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "volume", id.FormatVolumeID(volumeID), "create", auditStatusFor(err, "success"), mergeMeta(meta, err)))
	l.publish(ctx, events.Event{
		Event:     events.VolumeCreate,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatVolumeID(volumeID), Type: "volume"},
		Metadata:  map[string]string{"name": name},
		Error:     errString(err),
	})
}

// LogVolumeDelete records the deletion of an external storage volume.
func (l *AuditLogger) LogVolumeDelete(ctx context.Context, ac auth.AuthContext, volumeID pgtype.UUID, err error) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "volume", id.FormatVolumeID(volumeID), "delete", auditStatusFor(err, "warning"), mergeMeta(nil, err)))
	l.publish(ctx, events.Event{
		Event:     events.VolumeDelete,
		Outcome:   outcomeFromErr(err),
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(ac.TeamID),
		Actor:     actorToEvent(ac),
		Resource:  events.Resource{ID: id.FormatVolumeID(volumeID), Type: "volume"},
		Error:     errString(err),
	})
}

// --- Team events (scope: team) ---

func (l *AuditLogger) LogTeamRename(ctx context.Context, ac auth.AuthContext, teamID pgtype.UUID, oldName, newName string) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "team", id.FormatTeamID(teamID), "rename", "info", map[string]any{"old_name": oldName, "new_name": newName}))
}

func (l *AuditLogger) LogTeamSlugChange(ctx context.Context, ac auth.AuthContext, teamID pgtype.UUID, oldSlug, newSlug string) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "team", id.FormatTeamID(teamID), "slug_change", "info", map[string]any{"old_slug": oldSlug, "new_slug": newSlug}))
}

// LogTemplateRename records a team renaming one of its own templates.
func (l *AuditLogger) LogTemplateRename(ctx context.Context, ac auth.AuthContext, oldName, newName string) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "template", oldName, "rename", "info", map[string]any{"old_name": oldName, "new_name": newName}))
}

// LogTemplateRenameAdmin records an admin renaming a platform template.
func (l *AuditLogger) LogTemplateRenameAdmin(ctx context.Context, ac auth.AuthContext, oldName, newName string) {
	l.Log(ctx, newAdminEntry(ac, "template", oldName, "rename", "info", map[string]any{"old_name": oldName, "new_name": newName}))
}

func (l *AuditLogger) LogTemplateVisibility(ctx context.Context, ac auth.AuthContext, name string, public bool) {
	action := "unpublish"
	if public {
		action = "publish"
	}
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "template", name, action, "info", map[string]any{"public": public}))
}

// --- Channel events (scope: team) ---

func (l *AuditLogger) LogChannelCreate(ctx context.Context, ac auth.AuthContext, channelID pgtype.UUID, name, provider string) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "channel", id.FormatChannelID(channelID), "create", "success", map[string]any{"name": name, "provider": provider}))
}

func (l *AuditLogger) LogChannelUpdate(ctx context.Context, ac auth.AuthContext, channelID pgtype.UUID) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "channel", id.FormatChannelID(channelID), "update", "info", nil))
}

func (l *AuditLogger) LogChannelRotateConfig(ctx context.Context, ac auth.AuthContext, channelID pgtype.UUID) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "channel", id.FormatChannelID(channelID), "rotate_config", "info", nil))
}

func (l *AuditLogger) LogChannelDelete(ctx context.Context, ac auth.AuthContext, channelID pgtype.UUID) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "channel", id.FormatChannelID(channelID), "delete", "warning", nil))
}

// --- API key events (scope: team) ---

func (l *AuditLogger) LogAPIKeyCreate(ctx context.Context, ac auth.AuthContext, keyID pgtype.UUID, keyName string) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "api_key", id.FormatAPIKeyID(keyID), "create", "success", map[string]any{"name": keyName}))
}

func (l *AuditLogger) LogAPIKeyRevoke(ctx context.Context, ac auth.AuthContext, keyID pgtype.UUID) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "team", "api_key", id.FormatAPIKeyID(keyID), "revoke", "warning", nil))
}

// --- Member events (scope: admin) ---

func (l *AuditLogger) LogMemberAdd(ctx context.Context, ac auth.AuthContext, targetUserID pgtype.UUID, targetEmail, role string) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "admin", "member", id.FormatUserID(targetUserID), "add", "success", map[string]any{"email": targetEmail, "role": role}))
}

func (l *AuditLogger) LogMemberRemove(ctx context.Context, ac auth.AuthContext, targetUserID pgtype.UUID) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "admin", "member", id.FormatUserID(targetUserID), "remove", "warning", nil))
}

func (l *AuditLogger) LogMemberLeave(ctx context.Context, ac auth.AuthContext) {
	resourceID := ""
	if ac.UserID.Valid {
		resourceID = id.FormatUserID(ac.UserID)
	}
	l.Log(ctx, newEntry(ac, ac.TeamID, "admin", "member", resourceID, "leave", "info", nil))
}

func (l *AuditLogger) LogMemberRoleUpdate(ctx context.Context, ac auth.AuthContext, targetUserID pgtype.UUID, newRole string) {
	l.Log(ctx, newEntry(ac, ac.TeamID, "admin", "member", id.FormatUserID(targetUserID), "role_update", "info", map[string]any{"new_role": newRole}))
}

// --- Host events (scope: admin) ---

// LogHostCreate records a user-initiated host registration.
// BYOC hosts log to the owning team; shared hosts log to the platform team.
func (l *AuditLogger) LogHostCreate(ctx context.Context, ac auth.AuthContext, hostID, teamID pgtype.UUID) {
	l.Log(ctx, newEntry(ac, resolveHostTeamID(teamID), "admin", "host", id.FormatHostID(hostID), "create", "success", nil))
}

// LogHostDelete records a user-initiated host removal.
// BYOC hosts log to the owning team; shared hosts log to the platform team.
func (l *AuditLogger) LogHostDelete(ctx context.Context, ac auth.AuthContext, hostID, teamID pgtype.UUID) {
	l.Log(ctx, newEntry(ac, resolveHostTeamID(teamID), "admin", "host", id.FormatHostID(hostID), "delete", "warning", nil))
}

// LogHostMarkedDown records a system-initiated host status transition to unreachable.
// Scoped to "team" so BYOC team members can see when their hosts go down.
func (l *AuditLogger) LogHostMarkedDown(ctx context.Context, teamID, hostID pgtype.UUID) {
	l.logSystemHostEvent(ctx, teamID, hostID, "marked_down", "error", events.HostDown)
}

// LogHostMarkedUp records a system-initiated host status transition back to online.
// Scoped to "team" so BYOC team members can see when their hosts recover.
func (l *AuditLogger) LogHostMarkedUp(ctx context.Context, teamID, hostID pgtype.UUID) {
	l.logSystemHostEvent(ctx, teamID, hostID, "marked_up", "success", events.HostUp)
}

func (l *AuditLogger) logSystemHostEvent(ctx context.Context, teamID, hostID pgtype.UUID, action, status, ev string) {
	if !teamID.Valid {
		return
	}
	l.Log(ctx, Entry{
		TeamID: teamID, ActorType: "system",
		ResourceType: "host", ResourceID: id.FormatHostID(hostID),
		Action: action, Scope: "team", Status: status,
	})
	l.publish(ctx, events.Event{
		Event:     ev,
		Timestamp: events.Now(),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     events.SystemActor(),
		Resource:  events.Resource{ID: id.FormatHostID(hostID), Type: "host"},
	})
}

// --- User events (scope: admin) ---

func (l *AuditLogger) LogUserActivate(ctx context.Context, ac auth.AuthContext, userID pgtype.UUID, email string) {
	l.Log(ctx, newAdminEntry(ac, "user", id.FormatUserID(userID), "activate", "success", map[string]any{"email": email}))
}

func (l *AuditLogger) LogUserDeactivate(ctx context.Context, ac auth.AuthContext, userID pgtype.UUID, email string) {
	l.Log(ctx, newAdminEntry(ac, "user", id.FormatUserID(userID), "deactivate", "warning", map[string]any{"email": email}))
}

func (l *AuditLogger) LogUserGrantAdmin(ctx context.Context, ac auth.AuthContext, userID pgtype.UUID, email string) {
	l.Log(ctx, newAdminEntry(ac, "user", id.FormatUserID(userID), "grant_admin", "success", map[string]any{"email": email}))
}

func (l *AuditLogger) LogUserRevokeAdmin(ctx context.Context, ac auth.AuthContext, userID pgtype.UUID, email string) {
	l.Log(ctx, newAdminEntry(ac, "user", id.FormatUserID(userID), "revoke_admin", "warning", map[string]any{"email": email}))
}

// --- Team admin events (scope: admin) ---

func (l *AuditLogger) LogTeamSetBYOC(ctx context.Context, ac auth.AuthContext, teamID pgtype.UUID, enabled bool) {
	l.Log(ctx, newAdminEntry(ac, "team", id.FormatTeamID(teamID), "set_byoc", "info", map[string]any{"enabled": enabled}))
}

func (l *AuditLogger) LogTeamDelete(ctx context.Context, ac auth.AuthContext, teamID pgtype.UUID) {
	l.Log(ctx, newAdminEntry(ac, "team", id.FormatTeamID(teamID), "delete", "warning", nil))
}

// --- Template events (scope: admin) ---

func (l *AuditLogger) LogTemplateDelete(ctx context.Context, ac auth.AuthContext, name string) {
	l.Log(ctx, newAdminEntry(ac, "template", name, "delete", "warning", nil))
}

// --- Build events (scope: admin) ---

func (l *AuditLogger) LogBuildCreate(ctx context.Context, ac auth.AuthContext, buildID pgtype.UUID, name string) {
	l.Log(ctx, newAdminEntry(ac, "build", id.FormatBuildID(buildID), "create", "success", map[string]any{"name": name}))
}

func (l *AuditLogger) LogBuildCancel(ctx context.Context, ac auth.AuthContext, buildID pgtype.UUID) {
	l.Log(ctx, newAdminEntry(ac, "build", id.FormatBuildID(buildID), "cancel", "warning", nil))
}
