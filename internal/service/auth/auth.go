// MOCK: there is no separate user/auth service: no sign-up and no passwords.
// Tokens are issued by cmd/devtoken with the same secret. In production an auth
// service would issue them after checking the password (argon2id/bcrypt), with a
// short-lived access token and a refresh token, and services would verify the
// signature with an asymmetric key (RS256/EdDSA) without knowing the signing secret.
package auth

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"highload-sis2/internal/apperr"
)

type ctxKey struct{}

func WithUser(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, ctxKey{}, userID)
}

func UserID(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(ctxKey{}).(int64)
	return id, ok
}

func Issue(secret []byte, userID int64, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(userID, 10),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return s, nil
}

func Parse(secret []byte, token string) (int64, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		return 0, apperr.Wrap(apperr.KindUnauthenticated, err, "invalid token")
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || userID <= 0 {
		return 0, apperr.New(apperr.KindUnauthenticated, "invalid subject in token")
	}
	return userID, nil
}

func bearer(header string) (string, error) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", apperr.New(apperr.KindUnauthenticated, "missing bearer token")
	}
	return strings.TrimSpace(header[len(prefix):]), nil
}

func Authenticate(ctx context.Context, secret []byte, header string) (context.Context, error) {
	token, err := bearer(header)
	if err != nil {
		return nil, err
	}
	userID, err := Parse(secret, token)
	if err != nil {
		return nil, err
	}
	return WithUser(ctx, userID), nil
}
