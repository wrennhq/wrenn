package api

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
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
