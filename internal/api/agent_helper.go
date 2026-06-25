package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
	"git.omukk.dev/wrenn/wrenn/proto/hostagent/gen/hostagentv1connect"
)

// agentForHost looks up the host record and returns a Connect RPC client for it.
// Returns an error if the host is not found or has no address.
func agentForHost(ctx context.Context, queries *db.Queries, pool *lifecycle.HostClientPool, hostID pgtype.UUID) (hostagentv1connect.HostAgentServiceClient, error) {
	host, err := queries.GetHost(ctx, hostID)
	if err != nil {
		return nil, fmt.Errorf("host not found: %w", err)
	}
	return pool.GetForHost(host)
}

// requireRunningSandbox parses the sandbox ID from the URL, looks it up by team,
// and verifies it is running. On failure it writes the appropriate HTTP error and
// returns false.
func requireRunningSandbox(w http.ResponseWriter, r *http.Request, queries *db.Queries, teamID pgtype.UUID) (db.Sandbox, pgtype.UUID, string, bool) {
	sandboxIDStr := chi.URLParam(r, "id")
	ctx := r.Context()

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return db.Sandbox{}, pgtype.UUID{}, "", false
	}

	sb, err := queries.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return db.Sandbox{}, pgtype.UUID{}, "", false
	}
	if sb.Status != "running" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox is not running (status: "+sb.Status+")")
		return db.Sandbox{}, pgtype.UUID{}, "", false
	}

	return sb, sandboxID, sandboxIDStr, true
}

// upgradeAndAuthenticate upgrades the HTTP connection to WebSocket. The
// auth context must already be populated by upstream middleware — browser
// clients via the wrenn_sid cookie (sent automatically on WS upgrade),
// SDK clients via X-API-Key. Requests without an auth context are rejected
// with a 401 before the upgrade.
func upgradeAndAuthenticate(w http.ResponseWriter, r *http.Request) (*websocket.Conn, auth.AuthContext, error) {
	return upgradeAndAuthenticateWith(w, r, &upgrader)
}

// upgradeAndAuthenticateWith is upgradeAndAuthenticate with a caller-supplied
// upgrader, used by the PTY handler to negotiate per-message compression on that
// endpoint alone without affecting the other WebSocket routes.
func upgradeAndAuthenticateWith(w http.ResponseWriter, r *http.Request, up *websocket.Upgrader) (*websocket.Conn, auth.AuthContext, error) {
	ac, hasAuth := auth.FromContext(r.Context())
	if !hasAuth {
		writeError(w, http.StatusUnauthorized, "unauthorized", "session cookie or X-API-Key required")
		return nil, auth.AuthContext{}, fmt.Errorf("unauthenticated")
	}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return nil, auth.AuthContext{}, fmt.Errorf("websocket upgrade: %w", err)
	}
	return conn, ac, nil
}

// resolveTemplateSizes queries a host agent for the actual disk usage of any
// templates with size_bytes <= 0 (e.g. system base templates seeded with
// size_bytes = 0 before the rootfs was built). Results are persisted to the
// DB so subsequent requests serve the correct size without an RPC call.
// Errors are logged but do not prevent the caller from serving the templates.
func resolveTemplateSizes(ctx context.Context, queries *db.Queries, pool *lifecycle.HostClientPool, templates []db.Template) []db.Template {
	needResolve := false
	for _, t := range templates {
		if t.SizeBytes <= 0 {
			needResolve = true
			break
		}
	}
	if !needResolve {
		return templates
	}

	hosts, err := queries.ListActiveHosts(ctx)
	if err != nil || len(hosts) == 0 {
		slog.Warn("resolveTemplateSizes: no active hosts available", "error", err)
		return templates
	}

	agent, err := pool.GetForHost(hosts[0])
	if err != nil {
		slog.Warn("resolveTemplateSizes: failed to connect to host",
			"host_id", id.UUIDString(hosts[0].ID), "error", err)
		return templates
	}

	for i, t := range templates {
		if t.SizeBytes > 0 {
			continue
		}
		resp, err := agent.GetTemplateSize(ctx, connect.NewRequest(&pb.GetTemplateSizeRequest{
			TeamId:     formatUUIDForRPC(t.TeamID),
			TemplateId: formatUUIDForRPC(t.ID),
		}))
		if err != nil {
			slog.Warn("resolveTemplateSizes: failed to get size from host",
				"template", t.Name, "error", err)
			continue
		}
		templates[i].SizeBytes = resp.Msg.SizeBytes
		if err := queries.UpdateTemplateSize(ctx, db.UpdateTemplateSizeParams{
			ID:        t.ID,
			SizeBytes: resp.Msg.SizeBytes,
		}); err != nil {
			slog.Warn("resolveTemplateSizes: failed to persist size",
				"template", t.Name, "error", err)
		}
	}
	return templates
}

// updateLastActive updates the sandbox last_active_at timestamp.
// Uses a background context with timeout for streaming handlers where
// the request context may already be cancelled.
func updateLastActive(queries *db.Queries, sandboxID pgtype.UUID, sandboxIDStr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := queries.UpdateLastActive(ctx, db.UpdateLastActiveParams{
		ID: sandboxID,
		LastActiveAt: pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		},
	}); err != nil {
		slog.Warn("failed to update last_active_at", "id", sandboxIDStr, "error", err)
	}
}
