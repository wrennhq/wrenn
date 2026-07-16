package apperr

import (
	"context"
	"errors"
)

// isContextTimeout reports whether err is a context deadline or cancellation.
// Both resolve to Timeout: a cancelled request context reaches error mapping
// only when the work was cut short, which the client should treat the same
// as a timeout.
func isContextTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
