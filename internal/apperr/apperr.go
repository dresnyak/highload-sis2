package apperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Kind int

const (
	KindInternal Kind = iota
	KindInvalid
	KindIdempotencyMismatch
	KindUnauthenticated
	KindPermissionDenied
	KindNotFound
	KindVersionConflict
	KindRateLimited
	KindGroupFull
	KindUnavailable
	KindDeadline
	KindCanceled
)

type Error struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Msg, e.Err)
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

func New(kind Kind, format string, args ...any) error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

func Wrap(kind Kind, err error, msg string) error {
	return &Error{Kind: kind, Msg: msg, Err: err}
}

func KindOf(err error) Kind {
	if err == nil {
		return KindInternal
	}
	var ae *Error
	if errors.As(err, &ae) {
		return ae.Kind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return KindDeadline
	}
	if errors.Is(err, context.Canceled) {
		return KindCanceled
	}
	var pgErr *pgconn.PgError

	if errors.As(err, &pgErr) && pgErr.Code == "57014" {
		return KindDeadline
	}
	if s, ok := status.FromError(err); ok {
		return kindFromCode(s.Code())
	}
	return KindInternal
}

func GRPCCode(kind Kind) codes.Code {
	switch kind {
	case KindInvalid, KindIdempotencyMismatch:
		return codes.InvalidArgument
	case KindUnauthenticated:
		return codes.Unauthenticated
	case KindPermissionDenied:
		return codes.PermissionDenied
	case KindNotFound:
		return codes.NotFound
	case KindVersionConflict:
		return codes.Aborted
	case KindRateLimited, KindGroupFull:
		return codes.ResourceExhausted
	case KindUnavailable:
		return codes.Unavailable
	case KindDeadline:
		return codes.DeadlineExceeded
	case KindCanceled:
		return codes.Canceled
	default:
		return codes.Internal
	}
}

func HTTPStatus(kind Kind) int {
	switch kind {
	case KindInvalid:
		return http.StatusBadRequest
	case KindIdempotencyMismatch:
		return http.StatusUnprocessableEntity
	case KindUnauthenticated:
		return http.StatusUnauthorized
	case KindPermissionDenied:
		return http.StatusForbidden
	case KindNotFound:
		return http.StatusNotFound
	case KindVersionConflict:
		return http.StatusPreconditionFailed
	case KindRateLimited:
		return http.StatusTooManyRequests
	case KindGroupFull:
		return http.StatusConflict
	case KindUnavailable:
		return http.StatusServiceUnavailable
	case KindDeadline:
		return http.StatusGatewayTimeout
	case KindCanceled:
		return 499
	default:
		return http.StatusInternalServerError
	}
}

func ToGRPC(err error) error {
	if err == nil {
		return nil
	}
	var ae *Error
	if !errors.As(err, &ae) {
		if s, ok := status.FromError(err); ok {
			return s.Err()
		}
	}
	kind := KindOf(err)
	msg := err.Error()
	if kind == KindInternal {
		msg = "internal error"
	}
	return status.Error(GRPCCode(kind), msg)
}

func FromGRPC(err error, msg string) error {
	if err == nil {
		return nil
	}
	s, ok := status.FromError(err)
	if !ok {
		return Wrap(KindOf(err), err, msg)
	}
	return &Error{Kind: kindFromCode(s.Code()), Msg: msg + ": " + s.Message()}
}

func kindFromCode(c codes.Code) Kind {
	switch c {
	case codes.InvalidArgument:
		return KindInvalid
	case codes.Unauthenticated:
		return KindUnauthenticated
	case codes.PermissionDenied:
		return KindPermissionDenied
	case codes.NotFound:
		return KindNotFound
	case codes.Aborted:
		return KindVersionConflict
	case codes.ResourceExhausted:
		return KindRateLimited
	case codes.Unavailable:
		return KindUnavailable
	case codes.DeadlineExceeded:
		return KindDeadline
	case codes.Canceled:
		return KindCanceled
	default:
		return KindInternal
	}
}
