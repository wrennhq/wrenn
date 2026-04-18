package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// isWebSocketUpgrade returns true if the request is a WebSocket upgrade.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// ctxKeyAdminWS is a context key for flagging admin WS routes.
type ctxKeyAdminWS struct{}

// setAdminWSFlag marks the context as an admin WebSocket route.
func setAdminWSFlag(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyAdminWS{}, true)
}

// isAdminWSRoute checks if the request context was marked as admin WS.
func isAdminWSRoute(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyAdminWS{}).(bool)
	return v
}

// wsAuthMsg is the first message a browser WS client sends to authenticate.
type wsAuthMsg struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

// wsAuthenticate reads a JWT auth message from the WebSocket and returns the
// authenticated context. The caller must send this as the first message after
// connecting.
func wsAuthenticate(ctx context.Context, conn *websocket.Conn, jwtSecret []byte, queries *db.Queries) (auth.AuthContext, error) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var msg wsAuthMsg
	if err := conn.ReadJSON(&msg); err != nil {
		return auth.AuthContext{}, fmt.Errorf("read auth message: %w", err)
	}

	_ = conn.SetReadDeadline(time.Time{}) // clear deadline

	if msg.Type != "auth" || msg.Token == "" {
		return auth.AuthContext{}, fmt.Errorf("first message must be type 'auth' with a token")
	}

	claims, err := auth.VerifyJWT(jwtSecret, msg.Token)
	if err != nil {
		return auth.AuthContext{}, fmt.Errorf("invalid or expired token: %w", err)
	}

	teamID, err := id.ParseTeamID(claims.TeamID)
	if err != nil {
		return auth.AuthContext{}, fmt.Errorf("invalid team ID in token: %w", err)
	}

	userID, err := id.ParseUserID(claims.Subject)
	if err != nil {
		return auth.AuthContext{}, fmt.Errorf("invalid user ID in token: %w", err)
	}

	user, err := queries.GetUserByID(ctx, userID)
	if err != nil {
		return auth.AuthContext{}, fmt.Errorf("user not found")
	}
	if user.Status != "active" {
		return auth.AuthContext{}, fmt.Errorf("account deactivated")
	}

	return auth.AuthContext{
		TeamID: teamID,
		UserID: userID,
		Email:  claims.Email,
		Name:   claims.Name,
		Role:   claims.Role,
	}, nil
}

// wsAuthenticateAdmin performs WS-based auth and verifies admin status,
// returning an AuthContext with the platform team ID.
func wsAuthenticateAdmin(ctx context.Context, conn *websocket.Conn, jwtSecret []byte, queries *db.Queries) (auth.AuthContext, error) {
	ac, err := wsAuthenticate(ctx, conn, jwtSecret, queries)
	if err != nil {
		return auth.AuthContext{}, err
	}

	user, err := queries.GetUserByID(ctx, ac.UserID)
	if err != nil {
		return auth.AuthContext{}, fmt.Errorf("user not found")
	}
	if !user.IsAdmin {
		return auth.AuthContext{}, fmt.Errorf("admin access required")
	}

	ac.TeamID = id.PlatformTeamID
	return ac, nil
}
