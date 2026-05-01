package api

import (
	"log/slog"
	"net/http"
	"strings"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// requireAPIKeyOrJWT accepts either X-API-Key header or Authorization: Bearer JWT.
// Both stamp TeamID into the request context via auth.AuthContext.
func requireAPIKeyOrJWT(queries *db.Queries, jwtSecret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try API key first.
			if key := r.Header.Get("X-API-Key"); key != "" {
				hash := auth.HashAPIKey(key)
				row, err := queries.GetAPIKeyByHash(r.Context(), hash)
				if err != nil {
					slog.Warn("api key auth failed", "prefix", auth.APIKeyPrefix(key), "ip", r.RemoteAddr)
					writeError(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
					return
				}

				if err := queries.UpdateAPIKeyLastUsed(r.Context(), row.ID); err != nil {
					slog.Warn("failed to update api key last_used", "key_id", id.FormatAPIKeyID(row.ID), "error", err)
				}

				ctx := auth.WithAuthContext(r.Context(), auth.AuthContext{
					TeamID:     row.TeamID,
					APIKeyID:   row.ID,
					APIKeyName: row.Name,
				})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Try JWT bearer token from Authorization header.
			tokenStr := ""
			if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
				tokenStr = strings.TrimPrefix(header, "Bearer ")
			}
			if tokenStr != "" {
				claims, err := auth.VerifyJWT(jwtSecret, tokenStr)
				if err != nil {
					slog.Warn("jwt auth failed", "error", err, "ip", r.RemoteAddr)
					writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
					return
				}

				teamID, err := id.ParseTeamID(claims.TeamID)
				if err != nil {
					writeError(w, http.StatusUnauthorized, "unauthorized", "invalid team ID in token")
					return
				}
				userID, err := id.ParseUserID(claims.Subject)
				if err != nil {
					writeError(w, http.StatusUnauthorized, "unauthorized", "invalid user ID in token")
					return
				}

				// Verify user is still active in the database.
				user, err := queries.GetUserByID(r.Context(), userID)
				if err != nil {
					slog.Warn("jwt auth: failed to look up user", "user_id", claims.Subject, "error", err)
					writeError(w, http.StatusUnauthorized, "unauthorized", "user not found")
					return
				}
				if user.Status != "active" {
					writeError(w, http.StatusForbidden, "account_deactivated", "your account has been deactivated — contact your administrator to regain access")
					return
				}

				ctx := auth.WithAuthContext(r.Context(), auth.AuthContext{
					TeamID: teamID,
					UserID: userID,
					Email:  claims.Email,
					Name:   claims.Name,
					Role:   claims.Role,
				})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			writeError(w, http.StatusUnauthorized, "unauthorized", "X-API-Key or Authorization: Bearer <token> required")
		})
	}
}

// optionalAPIKeyOrJWT is like requireAPIKeyOrJWT but does not reject
// unauthenticated requests. It injects auth context when valid credentials
// are present (supporting SDK clients that set X-API-Key on WebSocket
// upgrades) and passes through otherwise so the handler can authenticate
// after the WebSocket upgrade via the first message.
func optionalAPIKeyOrJWT(queries *db.Queries, jwtSecret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try API key.
			if key := r.Header.Get("X-API-Key"); key != "" {
				hash := auth.HashAPIKey(key)
				row, err := queries.GetAPIKeyByHash(r.Context(), hash)
				if err == nil {
					if err := queries.UpdateAPIKeyLastUsed(r.Context(), row.ID); err != nil {
						slog.Warn("failed to update api key last_used", "key_id", id.FormatAPIKeyID(row.ID), "error", err)
					}
					ctx := auth.WithAuthContext(r.Context(), auth.AuthContext{
						TeamID:     row.TeamID,
						APIKeyID:   row.ID,
						APIKeyName: row.Name,
					})
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// Try JWT bearer token.
			if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
				tokenStr := strings.TrimPrefix(header, "Bearer ")
				if claims, err := auth.VerifyJWT(jwtSecret, tokenStr); err == nil {
					if teamID, err := id.ParseTeamID(claims.TeamID); err == nil {
						if userID, err := id.ParseUserID(claims.Subject); err == nil {
							if user, err := queries.GetUserByID(r.Context(), userID); err == nil && user.Status == "active" {
								ctx := auth.WithAuthContext(r.Context(), auth.AuthContext{
									TeamID: teamID,
									UserID: userID,
									Email:  claims.Email,
									Name:   claims.Name,
									Role:   claims.Role,
								})
								next.ServeHTTP(w, r.WithContext(ctx))
								return
							}
						}
					}
				}
			}

			// No valid credentials — pass through for handler to authenticate.
			next.ServeHTTP(w, r)
		})
	}
}
