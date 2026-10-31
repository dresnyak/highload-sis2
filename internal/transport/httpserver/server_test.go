package httpserver_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
	"highload-sis2/internal/repository/cache"
	taskrepo "highload-sis2/internal/repository/task"
	"highload-sis2/internal/service/auth"
	"highload-sis2/internal/service/task"
	"highload-sis2/internal/testinfra"
	"highload-sis2/internal/transport/httpserver"
)

var secret = []byte("test-secret")

type onlyGroup1Member1 struct{}

func (onlyGroup1Member1) CheckMembership(_ context.Context, groupID, userID int64) (model.Role, error) {
	if groupID == 1 && userID == 1 {
		return model.RoleOwner, nil
	}
	return 0, apperr.New(apperr.KindPermissionDenied, "not a member")
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	pool := testinfra.Postgres(t)
	repo := taskrepo.NewRepo(pool, pool)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := cache.New(nil, repo, false, log)
	svc := task.NewService(repo, c, c, onlyGroup1Member1{})
	srv := httptest.NewServer(httpserver.New(svc, secret, log))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, url string, user int64, body string, headers ...string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if user > 0 {
		token, _ := auth.Issue(secret, user, time.Hour)
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestRESTContract(t *testing.T) {
	srv := newServer(t)
	key := uuid.NewString()
	body := `{"title":"report","priority":2}`

	resp, created := do(t, "POST", srv.URL+"/v1/tasks", 1, body, "Idempotency-Key", key)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %v", resp.StatusCode, created)
	}
	resp, again := do(t, "POST", srv.URL+"/v1/tasks", 1, body, "Idempotency-Key", key)
	if resp.StatusCode != http.StatusOK || again["id"] != created["id"] {
		t.Fatalf("retry: %d %v, want 200 with same id", resp.StatusCode, again)
	}
	resp, _ = do(t, "POST", srv.URL+"/v1/tasks", 1, `{"title":"other"}`, "Idempotency-Key", key)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("same key, other body: %d, want 422", resp.StatusCode)
	}
	resp, _ = do(t, "POST", srv.URL+"/v1/tasks", 1, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key: %d, want 400", resp.StatusCode)
	}

	id := strconv.FormatFloat(created["id"].(float64), 'f', 0, 64)
	resp, _ = do(t, "PATCH", srv.URL+"/v1/tasks/"+id, 1, `{"status":"completed"}`, "If-Match", `"1"`)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("patch: %d etag=%s", resp.StatusCode, resp.Header.Get("ETag"))
	}
	resp, problem := do(t, "PATCH", srv.URL+"/v1/tasks/"+id, 1, `{"status":"planned"}`, "If-Match", `"1"`)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale patch: %d, want 412", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" || problem["status"] != float64(412) {
		t.Fatalf("error is not RFC 7807: %s %v", ct, problem)
	}

	for range 2 {
		resp, _ = do(t, "DELETE", srv.URL+"/v1/tasks/"+id, 1, "")
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("delete: %d, want 204", resp.StatusCode)
		}
	}

	resp, _ = do(t, "GET", srv.URL+"/v1/groups/1/tasks", 2, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member group list: %d, want 403", resp.StatusCode)
	}
	resp, _ = do(t, "GET", srv.URL+"/v1/me/tasks", 0, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", resp.StatusCode)
	}
}
