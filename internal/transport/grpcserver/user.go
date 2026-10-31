package grpcserver

import (
	"context"
	"strconv"

	"google.golang.org/grpc/metadata"

	"highload-sis2/internal/apperr"
)

const UserIDHeader = "x-user-id"

// MOCK: gRPC does not verify a JWT and trusts x-user-id. In production the API Gateway
// would set it after checking the token, and services would only accept calls over mTLS.
func callerID(ctx context.Context) (int64, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	v := md.Get(UserIDHeader)
	if len(v) == 0 {
		return 0, apperr.ToGRPC(apperr.New(apperr.KindInvalid, "x-user-id metadata is required"))
	}
	id, err := strconv.ParseInt(v[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.ToGRPC(apperr.New(apperr.KindInvalid, "x-user-id must be a positive integer"))
	}
	return id, nil
}
