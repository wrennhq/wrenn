package auth

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

type contextKey int

const authCtxKey contextKey = 0

// AuthContext is stamped into request context by auth middleware.
type AuthContext struct {
	TeamID     pgtype.UUID
	UserID     pgtype.UUID // zero value (Valid=false) when authenticated via API key
	Email      string      // empty when authenticated via API key
	Name       string      // empty when authenticated via API key
	Role       string      // owner, admin, or member; empty when authenticated via API key
	IsAdmin    bool        // platform-level admin; always false when authenticated via API key
	APIKeyID   pgtype.UUID // populated when authenticated via API key; zero value for JWT auth
	APIKeyName string      // display name of the key, snapshotted at auth time; empty for JWT auth
}

// WithAuthContext returns a new context with the given AuthContext.
func WithAuthContext(ctx context.Context, a AuthContext) context.Context {
	return context.WithValue(ctx, authCtxKey, a)
}

// FromContext retrieves the AuthContext. Returns zero value and false if absent.
func FromContext(ctx context.Context) (AuthContext, bool) {
	a, ok := ctx.Value(authCtxKey).(AuthContext)
	return a, ok
}

// MustFromContext retrieves the AuthContext. Panics if absent — only call
// inside handlers behind auth middleware.
func MustFromContext(ctx context.Context) AuthContext {
	a, ok := FromContext(ctx)
	if !ok {
		panic("auth: MustFromContext called on unauthenticated request")
	}
	return a
}

const hostCtxKey contextKey = 1

// HostContext is stamped into request context by host token middleware.
type HostContext struct {
	HostID pgtype.UUID
}

// WithHostContext returns a new context with the given HostContext.
func WithHostContext(ctx context.Context, h HostContext) context.Context {
	return context.WithValue(ctx, hostCtxKey, h)
}

// HostFromContext retrieves the HostContext. Returns zero value and false if absent.
func HostFromContext(ctx context.Context) (HostContext, bool) {
	h, ok := ctx.Value(hostCtxKey).(HostContext)
	return h, ok
}

// MustHostFromContext retrieves the HostContext. Panics if absent — only call
// inside handlers behind host token middleware.
func MustHostFromContext(ctx context.Context) HostContext {
	h, ok := HostFromContext(ctx)
	if !ok {
		panic("auth: MustHostFromContext called on unauthenticated request")
	}
	return h
}
