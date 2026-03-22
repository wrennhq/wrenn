package api

import (
	"log/slog"
	"net/http"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
)

// requireAPIKey validates the X-API-Key header, looks up the SHA-256 hash in DB,
// and stamps TeamID into the request context.
func requireAPIKey(queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("X-API-Key")
			if key == "" {
				writeError(w, http.StatusUnauthorized, "unauthorized", "X-API-Key header required")
				return
			}

			hash := auth.HashAPIKey(key)
			row, err := queries.GetAPIKeyByHash(r.Context(), hash)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
				return
			}

			// Best-effort update of last_used timestamp.
			if err := queries.UpdateAPIKeyLastUsed(r.Context(), row.ID); err != nil {
				slog.Warn("failed to update api key last_used", "key_id", row.ID, "error", err)
			}

			ctx := auth.WithAuthContext(r.Context(), auth.AuthContext{TeamID: row.TeamID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
