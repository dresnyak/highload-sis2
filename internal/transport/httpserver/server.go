package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
	"highload-sis2/internal/service/auth"
	"highload-sis2/internal/service/task"
)

const RequestTimeout = 300 * time.Millisecond

const maxBodyBytes = 64 << 10

type Handler struct {
	svc *task.Service
	log *slog.Logger
}

func New(svc *task.Service, secret []byte, log *slog.Logger) http.Handler {
	h := &Handler{svc: svc, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tasks", h.createTask)
	mux.HandleFunc("GET /v1/tasks/{id}", h.getTask)
	mux.HandleFunc("PATCH /v1/tasks/{id}", h.updateTask)
	mux.HandleFunc("DELETE /v1/tasks/{id}", h.deleteTask)
	mux.HandleFunc("GET /v1/me/tasks", h.listMyTasks)
	mux.HandleFunc("GET /v1/groups/{id}/tasks", h.listGroupTasks)

	return withTimeout(authMiddleware(secret, h.writeError, mux))
}

func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
}

func withTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	userID, _ := auth.UserID(r.Context())
	var body createTaskJSON
	if err := decodeJSON(r, &body); err != nil {
		h.writeError(w, r, err)
		return
	}
	t, created, err := h.svc.CreateTask(r.Context(), userID, model.CreateInput{
		Title:          body.Title,
		Description:    body.Description,
		GroupID:        body.GroupID,
		Priority:       body.Priority,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	code := http.StatusOK
	if created {
		code = http.StatusCreated
		w.Header().Set("Location", "/v1/tasks/"+strconv.FormatInt(t.ID, 10))
	}
	h.writeTask(w, code, t)
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	userID, _ := auth.UserID(r.Context())
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	t, err := h.svc.GetTask(r.Context(), userID, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeTask(w, http.StatusOK, t)
}

func (h *Handler) updateTask(w http.ResponseWriter, r *http.Request) {
	userID, _ := auth.UserID(r.Context())
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	version, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var body updateTaskJSON
	if err := decodeJSON(r, &body); err != nil {
		h.writeError(w, r, err)
		return
	}
	in := model.UpdateInput{
		ID:              id,
		ExpectedVersion: version,
		Title:           body.Title,
		Description:     body.Description,
		Priority:        body.Priority,
	}
	if body.Status != nil {
		s := model.Status(*body.Status)
		in.Status = &s
	}
	t, err := h.svc.UpdateTask(r.Context(), userID, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeTask(w, http.StatusOK, t)
}

func (h *Handler) deleteTask(w http.ResponseWriter, r *http.Request) {
	userID, _ := auth.UserID(r.Context())
	id, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.DeleteTask(r.Context(), userID, id); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listMyTasks(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, model.ListFilter{Scope: model.ScopePersonal})
}

func (h *Handler) listGroupTasks(w http.ResponseWriter, r *http.Request) {
	gid, err := pathID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.list(w, r, model.ListFilter{Scope: model.ScopeGroup, GroupID: gid})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, f model.ListFilter) {
	userID, _ := auth.UserID(r.Context())
	if err := parseListQuery(r, &f); err != nil {
		h.writeError(w, r, err)
		return
	}
	res, err := h.svc.ListTasks(r.Context(), userID, f)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := listJSON{Tasks: make([]taskJSON, len(res.Tasks)), NextCursor: res.NextCursor}
	for i, t := range res.Tasks {
		out.Tasks[i] = toJSON(t)
	}
	writeJSON(w, http.StatusOK, out)
}

func parseListQuery(r *http.Request, f *model.ListFilter) error {
	q := r.URL.Query()
	for _, v := range q["status"] {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				f.Statuses = append(f.Statuses, model.Status(s))
			}
		}
	}
	switch q.Get("sort") {
	case "", "priority":
		f.SortBy = model.SortByPriority
	case "created_at":
		f.SortBy = model.SortByCreatedAt
	default:
		return apperr.New(apperr.KindInvalid, "sort must be priority or created_at")
	}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil {
			return apperr.New(apperr.KindInvalid, "limit must be a number")
		}
		f.PageSize = n
	}
	f.Cursor = q.Get("cursor")
	return nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.New(apperr.KindInvalid, "id must be a positive integer")
	}
	return id, nil
}

func parseIfMatch(v string) (int64, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "W/")
	v = strings.Trim(v, `"`)
	if v == "" {
		return 0, apperr.New(apperr.KindInvalid, "If-Match header with task version is required")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, apperr.New(apperr.KindInvalid, "If-Match must contain a task version")
	}
	return n, nil
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apperr.Wrap(apperr.KindInvalid, err, "invalid JSON body")
	}
	return nil
}

func (h *Handler) writeTask(w http.ResponseWriter, code int, t model.Task) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(t.Version, 10)+`"`)
	writeJSON(w, code, toJSON(t))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	kind := apperr.KindOf(err)
	code := apperr.HTTPStatus(kind)
	detail := err.Error()
	if kind == apperr.KindInternal {
		h.log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		detail = ""
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(problem{
		Type:   "about:blank",
		Title:  http.StatusText(code),
		Status: code,
		Detail: detail,
	})
}
