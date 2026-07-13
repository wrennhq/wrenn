package apperr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
)

// WriteHTTP(nil) must not panic on the From(nil)==nil deref; it should emit a
// generic 500 envelope rather than a bare 200 or a crash.
func TestWriteHTTPNil(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	WriteHTTP(rec, req, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	var body wireEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != Internal.Code {
		t.Errorf("code = %q, want %q", body.Error.Code, Internal.Code)
	}
}

func TestConnectRoundTrip(t *testing.T) {
	orig := SandboxNotRunning.
		Wrap(errors.New("boxes map miss")).
		With("status", "paused")

	ce := ToConnect(orig)
	if ce.Code() != connect.CodeFailedPrecondition {
		t.Errorf("connect code = %v, want FailedPrecondition", ce.Code())
	}

	got := From(fmt.Errorf("rpc CreateSandbox: %w", ce))
	if got.Code != orig.Code {
		t.Errorf("Code = %q, want %q", got.Code, orig.Code)
	}
	if got.Status != orig.Status {
		t.Errorf("Status = %d, want %d", got.Status, orig.Status)
	}
	if got.Message != orig.Message {
		t.Errorf("Message = %q, want %q", got.Message, orig.Message)
	}
	if got.Retryable != orig.Retryable {
		t.Errorf("Retryable = %v, want %v", got.Retryable, orig.Retryable)
	}
	if got.Details["status"] != "paused" {
		t.Errorf("Details = %v, want status=paused", got.Details)
	}
	// The internal cause must not surface in client-safe fields.
	if got.Message == orig.Error() {
		t.Error("cause chain leaked into Message")
	}
}

// Bad-gateway (502) catalog defs must map to a retryable Connect code, not the
// generic Internal, so observability and consumers keying off the raw wire code
// see a transient infrastructure failure rather than an internal-invariant bug.
func TestToConnectBadGateway(t *testing.T) {
	for _, def := range []Def{SandboxUnresponsive, HostUnreachable} {
		t.Run(def.Code, func(t *testing.T) {
			ce := ToConnect(def.Wrap(errors.New("envd dial: connection refused")))
			if ce.Code() != connect.CodeUnavailable {
				t.Errorf("connect code = %v, want Unavailable", ce.Code())
			}
			// The ErrorInfo detail keeps the round trip lossless.
			got := From(fmt.Errorf("rpc: %w", ce))
			if got.Code != def.Code {
				t.Errorf("Code = %q, want %q", got.Code, def.Code)
			}
			if got.Status != http.StatusBadGateway {
				t.Errorf("Status = %d, want 502", got.Status)
			}
			if !got.Retryable {
				t.Error("Retryable = false, want true")
			}
		})
	}
}

// Without an ErrorInfo detail, a Connect error did not come from a wrenn
// handler — it is a transport-level failure synthesized by the framework — so
// it resolves to internal_error regardless of its Connect code. The raw cause
// is hidden from the client but stays in the chain for logs. (The old
// guess-from-code mapping is gone.)
func TestFromConnectWithoutDetail(t *testing.T) {
	codes := []connect.Code{
		connect.CodeUnavailable,
		connect.CodeNotFound,
		connect.CodeInvalidArgument,
		connect.CodeFailedPrecondition,
		connect.CodePermissionDenied,
		connect.CodeUnauthenticated,
		connect.CodeResourceExhausted,
		connect.CodeUnimplemented,
		connect.CodeDeadlineExceeded,
		connect.CodeInternal,
		connect.CodeUnknown,
	}
	for _, code := range codes {
		t.Run(code.String(), func(t *testing.T) {
			ce := connect.NewError(code, errors.New("open /var/lib/wrenn/secret: boom"))
			got := From(ce)
			if got.Code != Internal.Code {
				t.Errorf("Code = %q, want %q", got.Code, Internal.Code)
			}
			if got.Message != Internal.Message {
				t.Errorf("raw cause leaked to client message: %q", got.Message)
			}
			if !errors.Is(got, error(ce)) {
				t.Error("connect error must remain in chain for logging")
			}
		})
	}
}

// A Connect error that genuinely wraps a context deadline still resolves to
// Timeout — matched on the cause chain, not guessed from the Connect code —
// so a real client-side timeout stays a retryable 504.
func TestFromContextDeadlineViaConnect(t *testing.T) {
	ce := connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded)
	got := From(ce)
	if got.Code != Timeout.Code {
		t.Errorf("Code = %q, want %q", got.Code, Timeout.Code)
	}
	if !got.Retryable {
		t.Error("Retryable = false, want true")
	}
}

// ToConnect(nil) must not panic on the From(nil)==nil deref; a nil error
// reaching here is a caller bug that should degrade to a generic internal
// error, not crash the RPC handler.
func TestToConnectNil(t *testing.T) {
	ce := ToConnect(nil)
	if ce.Code() != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", ce.Code())
	}
	if got := From(ce); got.Code != Internal.Code {
		t.Errorf("Code = %q, want %q", got.Code, Internal.Code)
	}
}

func TestToConnectFromPlainError(t *testing.T) {
	ce := ToConnect(errors.New("dmsetup create failed: device busy"))
	if ce.Code() != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", ce.Code())
	}
	got := From(ce)
	if got.Code != Internal.Code || got.Status != http.StatusInternalServerError {
		t.Errorf("got %q/%d, want internal_error/500", got.Code, got.Status)
	}
	if got.Message != Internal.Message {
		t.Errorf("internal cause leaked to client message: %q", got.Message)
	}
}
