package auth

import "context"

type contextKey int

const authCtxKey contextKey = 0

// AuthContext is stamped into request context by auth middleware.
type AuthContext struct {
	TeamID string
	UserID string // empty when authenticated via API key
	Email  string // empty when authenticated via API key
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
