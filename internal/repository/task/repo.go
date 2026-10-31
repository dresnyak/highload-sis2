package task

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
)

const queryTimeout = 80 * time.Millisecond

func NewPool(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(int(queryTimeout.Milliseconds()))
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	return pool, nil
}

type Repo struct {
	primary *pgxpool.Pool
	// MOCK: in docker-compose the replica points at the same PostgreSQL as the primary.
	// In production this is a separate streaming replica lagging by milliseconds.
	replica *pgxpool.Pool
}

func NewRepo(primary, replica *pgxpool.Pool) *Repo {
	return &Repo{primary: primary, replica: replica}
}

const taskColumns = `id, author_id, group_id, title, description, status, priority, version, created_at, updated_at`

func scanTask(row pgx.Row) (model.Task, error) {
	var t model.Task
	var status string
	err := row.Scan(&t.ID, &t.AuthorID, &t.GroupID, &t.Title, &t.Description,
		&status, &t.Priority, &t.Version, &t.CreatedAt, &t.UpdatedAt)
	t.Status = model.Status(status)
	return t, err
}

func begin(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	return ctx, cancel, nil
}

func (r *Repo) pool(fromPrimary bool) *pgxpool.Pool {
	if fromPrimary {
		return r.primary
	}
	return r.replica
}

func (r *Repo) Create(ctx context.Context, authorID int64, key uuid.UUID, in model.CreateInput, reqHash []byte) (model.Task, bool, error) {
	ctx, cancel, err := begin(ctx)
	if err != nil {
		return model.Task{}, false, err
	}
	defer cancel()

	row := r.primary.QueryRow(ctx, `
		INSERT INTO tasks (author_id, group_id, title, description, priority, client_request_id, request_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (author_id, client_request_id) DO NOTHING
		RETURNING `+taskColumns,
		authorID, in.GroupID, in.Title, in.Description, in.Priority, key, reqHash)
	t, err := scanTask(row)
	if err == nil {
		return t, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, false, fmt.Errorf("insert task: %w", err)
	}

	var storedHash []byte
	err = r.primary.QueryRow(ctx, `
		SELECT `+taskColumns+`, request_hash FROM tasks
		WHERE author_id = $1 AND client_request_id = $2`, authorID, key).
		Scan(&t.ID, &t.AuthorID, &t.GroupID, &t.Title, &t.Description,
			(*string)(&t.Status), &t.Priority, &t.Version, &t.CreatedAt, &t.UpdatedAt, &storedHash)
	if err != nil {
		return model.Task{}, false, fmt.Errorf("select task by idempotency key: %w", err)
	}
	if string(storedHash) != string(reqHash) {
		return model.Task{}, false, apperr.New(apperr.KindIdempotencyMismatch,
			"idempotency key was already used with a different request body")
	}

	return t, false, nil
}

func (r *Repo) Get(ctx context.Context, fromPrimary bool, id int64) (model.Task, error) {
	ctx, cancel, err := begin(ctx)
	if err != nil {
		return model.Task{}, err
	}
	defer cancel()

	t, err := scanTask(r.pool(fromPrimary).QueryRow(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE id = $1 AND deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, apperr.New(apperr.KindNotFound, "task %d not found", id)
	}
	if err != nil {
		return model.Task{}, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

func (r *Repo) GetForWrite(ctx context.Context, id int64) (model.Task, bool, error) {
	ctx, cancel, err := begin(ctx)
	if err != nil {
		return model.Task{}, false, err
	}
	defer cancel()

	var t model.Task
	var deletedAt *time.Time
	err = r.primary.QueryRow(ctx, `SELECT `+taskColumns+`, deleted_at FROM tasks WHERE id = $1`, id).
		Scan(&t.ID, &t.AuthorID, &t.GroupID, &t.Title, &t.Description,
			(*string)(&t.Status), &t.Priority, &t.Version, &t.CreatedAt, &t.UpdatedAt, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, false, apperr.New(apperr.KindNotFound, "task %d not found", id)
	}
	if err != nil {
		return model.Task{}, false, fmt.Errorf("get task for write: %w", err)
	}
	return t, deletedAt != nil, nil
}

func (r *Repo) Update(ctx context.Context, in model.UpdateInput) (model.Task, error) {
	ctx, cancel, err := begin(ctx)
	if err != nil {
		return model.Task{}, err
	}
	defer cancel()

	var status *string
	if in.Status != nil {
		s := string(*in.Status)
		status = &s
	}

	t, err := scanTask(r.primary.QueryRow(ctx, `
		UPDATE tasks SET
			title       = COALESCE($3, title),
			description = COALESCE($4, description),
			status      = COALESCE($5, status),
			priority    = COALESCE($6, priority),
			version     = version + 1,
			updated_at  = now()
		WHERE id = $1 AND version = $2 AND deleted_at IS NULL
		RETURNING `+taskColumns,
		in.ID, in.ExpectedVersion, in.Title, in.Description, status, in.Priority))
	if errors.Is(err, pgx.ErrNoRows) {

		return model.Task{}, apperr.New(apperr.KindVersionConflict,
			"task %d was modified concurrently: expected version %d", in.ID, in.ExpectedVersion)
	}
	if err != nil {
		return model.Task{}, fmt.Errorf("update task: %w", err)
	}
	return t, nil
}

func (r *Repo) SoftDelete(ctx context.Context, id int64) error {
	ctx, cancel, err := begin(ctx)
	if err != nil {
		return err
	}
	defer cancel()

	_, err = r.primary.Exec(ctx, `
		UPDATE tasks SET deleted_at = now(), version = version + 1, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("soft delete task: %w", err)
	}
	return nil
}

func (r *Repo) List(ctx context.Context, fromPrimary bool, f model.ListFilter) (model.ListResult, error) {
	ctx, cancel, err := begin(ctx)
	if err != nil {
		return model.ListResult{}, err
	}
	defer cancel()

	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	where = append(where, "deleted_at IS NULL")
	if f.Scope == model.ScopeGroup {
		where = append(where, "group_id = "+arg(f.GroupID))
	} else {
		where = append(where, "author_id = "+arg(f.AuthorID), "group_id IS NULL")
	}
	if len(f.Statuses) > 0 {
		ss := make([]string, len(f.Statuses))
		for i, s := range f.Statuses {
			ss[i] = string(s)
		}
		where = append(where, "status = ANY("+arg(ss)+")")
	}

	order := "priority DESC, id DESC"
	if f.SortBy == model.SortByCreatedAt {
		order = "created_at DESC, id DESC"
	}
	if f.Cursor != "" {
		c, err := model.DecodeCursor(f.Cursor)
		if err != nil {
			return model.ListResult{}, err
		}
		if c.Sort != f.SortBy {
			return model.ListResult{}, apperr.New(apperr.KindInvalid, "page token belongs to a different sort order")
		}

		if f.SortBy == model.SortByPriority {
			where = append(where, "(priority, id) < ("+arg(c.Priority)+", "+arg(c.ID)+")")
		} else {
			where = append(where, "(created_at, id) < ("+arg(c.CreatedAtTime())+", "+arg(c.ID)+")")
		}
	}

	query := `SELECT ` + taskColumns + ` FROM tasks WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY ` + order + ` LIMIT ` + arg(f.PageSize+1)

	rows, err := r.pool(fromPrimary).Query(ctx, query, args...)
	if err != nil {
		return model.ListResult{}, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]model.Task, 0, f.PageSize)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return model.ListResult{}, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return model.ListResult{}, fmt.Errorf("list tasks: %w", err)
	}

	var res model.ListResult
	if len(tasks) > f.PageSize {
		tasks = tasks[:f.PageSize]
		res.NextCursor = model.EncodeCursor(f.SortBy, tasks[len(tasks)-1])
	}
	res.Tasks = tasks
	return res, nil
}
