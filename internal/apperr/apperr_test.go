package apperr

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMapping(t *testing.T) {
	cases := []struct {
		err      error
		grpcCode codes.Code
		httpCode int
	}{
		{New(KindInvalid, "x"), codes.InvalidArgument, http.StatusBadRequest},
		{New(KindIdempotencyMismatch, "x"), codes.InvalidArgument, http.StatusUnprocessableEntity},
		{New(KindVersionConflict, "x"), codes.Aborted, http.StatusPreconditionFailed},
		{New(KindPermissionDenied, "x"), codes.PermissionDenied, http.StatusForbidden},
		{New(KindGroupFull, "x"), codes.ResourceExhausted, http.StatusConflict},
		{New(KindRateLimited, "x"), codes.ResourceExhausted, http.StatusTooManyRequests},
		{fmt.Errorf("query: %w", context.DeadlineExceeded), codes.DeadlineExceeded, http.StatusGatewayTimeout},
		{FromGRPC(status.Error(codes.Unavailable, "down"), "dep"), codes.Unavailable, http.StatusServiceUnavailable},
		{fmt.Errorf("boom"), codes.Internal, http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got := status.Code(ToGRPC(c.err)); got != c.grpcCode {
			t.Errorf("%v: grpc %v, want %v", c.err, got, c.grpcCode)
		}
		if got := HTTPStatus(KindOf(c.err)); got != c.httpCode {
			t.Errorf("%v: http %d, want %d", c.err, got, c.httpCode)
		}
	}
}
