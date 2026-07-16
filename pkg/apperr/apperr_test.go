package apperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestDefConstructors(t *testing.T) {
	d := Def{Code: "test_failed", Status: http.StatusTeapot, Retryable: true, Message: "default message"}

	tests := []struct {
		name        string
		err         *Error
		wantMessage string
		wantCause   bool
	}{
		{"New", d.New(), "default message", false},
		{"Msg", d.Msg("custom"), "custom", false},
		{"Msgf", d.Msgf("custom %d", 42), "custom 42", false},
		{"Wrap", d.Wrap(errors.New("boom")), "default message", true},
		{"WrapMsg", d.WrapMsg(errors.New("boom"), "custom"), "custom", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code != "test_failed" || tt.err.Status != http.StatusTeapot || !tt.err.Retryable {
				t.Errorf("def fields not carried: %+v", tt.err)
			}
			if tt.err.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", tt.err.Message, tt.wantMessage)
			}
			if (tt.err.Cause() != nil) != tt.wantCause {
				t.Errorf("Cause() = %v, wantCause %v", tt.err.Cause(), tt.wantCause)
			}
		})
	}
}

func TestErrorsIsMatchesOnCode(t *testing.T) {
	d := Def{Code: "thing_missing", Status: 404, Message: "not found"}
	other := Def{Code: "other_code", Status: 404, Message: "not found"}

	wrapped := fmt.Errorf("outer: %w", d.Msg("different message"))
	if !errors.Is(wrapped, d.New()) {
		t.Error("errors.Is should match same code through wrapping")
	}
	if errors.Is(wrapped, other.New()) {
		t.Error("errors.Is should not match different code")
	}
	if !d.Is(wrapped) {
		t.Error("Def.Is should match same code through wrapping")
	}
	if other.Is(wrapped) {
		t.Error("Def.Is should not match different code")
	}
}

func TestWithDoesNotMutateOriginal(t *testing.T) {
	d := Def{Code: "x", Status: 400, Message: "m"}
	base := d.New().With("a", 1)
	derived := base.With("b", 2)

	if len(base.Details) != 1 {
		t.Errorf("base details mutated: %v", base.Details)
	}
	if derived.Details["a"] != 1 || derived.Details["b"] != 2 {
		t.Errorf("derived details wrong: %v", derived.Details)
	}
}

func TestFrom(t *testing.T) {
	d := Def{Code: "known", Status: 404, Message: "known thing missing"}

	t.Run("nil", func(t *testing.T) {
		if From(nil) != nil {
			t.Error("From(nil) should be nil")
		}
	})

	t.Run("passthrough", func(t *testing.T) {
		e := d.Wrap(errors.New("cause"))
		got := From(fmt.Errorf("outer: %w", e))
		if got.Code != "known" {
			t.Errorf("Code = %q, want known", got.Code)
		}
	})

	t.Run("context deadline", func(t *testing.T) {
		got := From(fmt.Errorf("op: %w", context.DeadlineExceeded))
		if got.Code != Timeout.Code {
			t.Errorf("Code = %q, want %q", got.Code, Timeout.Code)
		}
	})

	t.Run("unknown becomes internal", func(t *testing.T) {
		cause := errors.New("db exploded at /var/lib/secret")
		got := From(cause)
		if got.Code != Internal.Code {
			t.Errorf("Code = %q, want %q", got.Code, Internal.Code)
		}
		if got.Message != Internal.Message {
			t.Errorf("internal error must not leak cause; Message = %q", got.Message)
		}
		if !errors.Is(got, cause) {
			t.Error("cause must be preserved in chain for logging")
		}
	})
}

func TestHTTPStatusDefaultsTo500(t *testing.T) {
	e := &Error{Code: "misconstructed"}
	if e.HTTPStatus() != http.StatusInternalServerError {
		t.Errorf("HTTPStatus = %d, want 500", e.HTTPStatus())
	}
}
