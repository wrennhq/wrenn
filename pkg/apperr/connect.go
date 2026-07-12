package apperr

import (
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

// ToConnect converts any error to a *connect.Error carrying an ErrorInfo
// detail. The RPC error message keeps the full cause chain (the CP↔agent
// channel is internal, and the receiver logs it); the ErrorInfo detail holds
// only the client-safe fields, which the receiving side re-surfaces via From.
func ToConnect(err error) *connect.Error {
	e := From(err)
	if e == nil {
		// Defensive: a nil error reaching here is a caller bug. Return a
		// generic internal error rather than panicking on the nil deref.
		e = Internal.New()
	}
	ce := connect.NewError(connectCodeFor(e.HTTPStatus()), errors.New(e.Error()))
	if detail, derr := connect.NewErrorDetail(errorInfo(e)); derr == nil {
		ce.AddDetail(detail)
	}
	return ce
}

func errorInfo(e *Error) *pb.ErrorInfo {
	info := &pb.ErrorInfo{
		Code:       e.Code,
		Message:    e.Message,
		Retryable:  e.Retryable,
		HttpStatus: int32(e.HTTPStatus()),
	}
	if len(e.Details) > 0 {
		info.Details = make(map[string]string, len(e.Details))
		for k, v := range e.Details {
			info.Details[k] = fmt.Sprint(v)
		}
	}
	return info
}

func asConnectError(err error) *connect.Error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	return nil
}

// fromConnect resolves a Connect RPC error to an *Error. Every wrenn service
// attaches an ErrorInfo detail via ToConnect, so an error carrying one is
// reconstructed losslessly. An error WITHOUT a detail did not originate from a
// wrenn handler — it is a transport-level failure synthesized by the Connect
// framework itself (a dropped connection, a client-side deadline) — so it
// resolves to internal_error, with the raw RPC error hidden from the client
// but kept in the chain for logs.
func fromConnect(ce *connect.Error) *Error {
	for _, d := range ce.Details() {
		msg, err := d.Value()
		if err != nil {
			continue
		}
		if info, ok := msg.(*pb.ErrorInfo); ok {
			e := &Error{
				Code:      info.Code,
				Status:    int(info.HttpStatus),
				Message:   info.Message,
				Retryable: info.Retryable,
				cause:     ce,
			}
			if e.Status == 0 {
				if d, ok := Lookup(info.Code); ok {
					e.Status = d.Status
				} else {
					e.Status = http.StatusInternalServerError
				}
			}
			if len(info.Details) > 0 {
				e.Details = make(map[string]any, len(info.Details))
				for k, v := range info.Details {
					e.Details[k] = v
				}
			}
			return e
		}
	}
	return Internal.Wrap(ce)
}

// connectCodeFor maps an HTTP status to the Connect code used on the wire.
func connectCodeFor(status int) connect.Code {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return connect.CodeInvalidArgument
	case http.StatusUnauthorized:
		return connect.CodeUnauthenticated
	case http.StatusForbidden:
		return connect.CodePermissionDenied
	case http.StatusNotFound:
		return connect.CodeNotFound
	case http.StatusConflict:
		return connect.CodeFailedPrecondition
	case http.StatusTooManyRequests:
		return connect.CodeResourceExhausted
	case http.StatusNotImplemented:
		return connect.CodeUnimplemented
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return connect.CodeUnavailable
	case http.StatusGatewayTimeout:
		return connect.CodeDeadlineExceeded
	default:
		return connect.CodeInternal
	}
}
