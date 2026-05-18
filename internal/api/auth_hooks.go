package api

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/cpextension"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// collectAuthHooks filters the extension list down to those that implement
// cpextension.AuthHook.
func collectAuthHooks(extensions []cpextension.Extension) []cpextension.AuthHook {
	var hooks []cpextension.AuthHook
	for _, ext := range extensions {
		if h, ok := ext.(cpextension.AuthHook); ok {
			hooks = append(hooks, h)
		}
	}
	return hooks
}

// fireOnSignup runs every OnSignup hook sequentially. The first error wins —
// signup must abort so the cloud-side billing customer creation stays the
// gating step. Other hook errors after a failure are not run.
func fireOnSignup(ctx context.Context, hooks []cpextension.AuthHook, userID, teamID pgtype.UUID, email string) error {
	for _, h := range hooks {
		if err := h.OnSignup(ctx, userID, teamID, email); err != nil {
			return err
		}
	}
	return nil
}

// fireOnLogin runs every OnLogin hook. Errors are logged and swallowed —
// login must never be blocked by a misbehaving extension.
func fireOnLogin(ctx context.Context, hooks []cpextension.AuthHook, userID pgtype.UUID) {
	for _, h := range hooks {
		if err := h.OnLogin(ctx, userID); err != nil {
			slog.Warn("auth hook OnLogin failed", "user_id", id.FormatUserID(userID), "error", err)
		}
	}
}

// fireOnSoftDelete runs every OnAccountSoftDelete hook. Errors are logged.
func fireOnSoftDelete(ctx context.Context, hooks []cpextension.AuthHook, userID pgtype.UUID) {
	for _, h := range hooks {
		if err := h.OnAccountSoftDelete(ctx, userID); err != nil {
			slog.Warn("auth hook OnAccountSoftDelete failed", "user_id", id.FormatUserID(userID), "error", err)
		}
	}
}
