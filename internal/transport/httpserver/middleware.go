package httpserver

import (
	"net/http"

	"highload-sis2/internal/service/auth"
)

type errorWriter func(w http.ResponseWriter, r *http.Request, err error)

func authMiddleware(secret []byte, writeErr errorWriter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := auth.Authenticate(r.Context(), secret, r.Header.Get("Authorization"))
		if err != nil {
			writeErr(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
