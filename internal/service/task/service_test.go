package task_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
	"highload-sis2/internal/repository/cache"
	taskrepo "highload-sis2/internal/repository/task"
	"highload-sis2/internal/service/task"
	"highload-sis2/internal/testinfra"
)

type fakeGroups map[int64]map[int64]model.Role

func (f fakeGroups) CheckMembership(_ context.Context, groupID, userID int64) (model.Role, error) {
	if role, ok := f[groupID][userID]; ok {
		return role, nil
	}
	return 0, apperr.New(apperr.KindPermissionDenied, "not a member")
}

func newService(t *testing.T, groups task.Membership) (*task.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testinfra.Postgres(t)
	repo := taskrepo.NewRepo(pool, pool)

	c := cache.New(nil, repo, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return task.NewService(repo, c, c, groups), pool
}

func countTasks(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func requireKind(t *testing.T, err error, want apperr.Kind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error of kind %d, got nil", want)
	}
	if got := apperr.KindOf(err); got != want {
		t.Fatalf("expected error kind %d, got %d (%v)", want, got, err)
	}
}

func TestCreateTaskIdempotent(t *testing.T) {
	svc, pool := newService(t, fakeGroups{})
	ctx := context.Background()
	in := model.CreateInput{Title: "write report", Priority: 2, IdempotencyKey: uuid.NewString()}

	first, created1, err := svc.CreateTask(ctx, 1, in)
	if err != nil {
		t.Fatal(err)
	}
	second, created2, err := svc.CreateTask(ctx, 1, in)
	if err != nil {
		t.Fatal(err)
	}

	if !created1 || created2 {
		t.Fatalf("created flags: first=%v second=%v, want true/false", created1, created2)
	}
	if first.ID != second.ID || first.Version != second.Version || !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("retry returned a different task: %+v vs %+v", first, second)
	}
	if n := countTasks(t, pool); n != 1 {
		t.Fatalf("rows in db = %d, want 1", n)
	}
}

func TestCreateTaskSameKeyDifferentBody(t *testing.T) {
	svc, pool := newService(t, fakeGroups{})
	ctx := context.Background()
	key := uuid.NewString()

	if _, _, err := svc.CreateTask(ctx, 1, model.CreateInput{Title: "a", IdempotencyKey: key}); err != nil {
		t.Fatal(err)
	}
	_, _, err := svc.CreateTask(ctx, 1, model.CreateInput{Title: "b", IdempotencyKey: key})
	requireKind(t, err, apperr.KindIdempotencyMismatch)
	if n := countTasks(t, pool); n != 1 {
		t.Fatalf("rows in db = %d, want 1", n)
	}
}

func TestUpdateTaskStaleVersion(t *testing.T) {
	svc, _ := newService(t, fakeGroups{})
	ctx := context.Background()
	created, _, err := svc.CreateTask(ctx, 1, model.CreateInput{Title: "t", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}

	status := model.StatusInProgress
	updated, err := svc.UpdateTask(ctx, 1, model.UpdateInput{ID: created.ID, ExpectedVersion: 1, Status: &status})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Status != model.StatusInProgress || updated.Title != "t" {
		t.Fatalf("unexpected update result: %+v", updated)
	}

	_, err = svc.UpdateTask(ctx, 1, model.UpdateInput{ID: created.ID, ExpectedVersion: 1, Status: &status})
	requireKind(t, err, apperr.KindVersionConflict)
}

func TestUpdateTaskClearsDescription(t *testing.T) {
	svc, _ := newService(t, fakeGroups{})
	ctx := context.Background()
	created, _, err := svc.CreateTask(ctx, 1, model.CreateInput{Title: "t", Description: "d", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	updated, err := svc.UpdateTask(ctx, 1, model.UpdateInput{ID: created.ID, ExpectedVersion: 1, Description: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Description != "" || updated.Title != "t" {
		t.Fatalf("partial update went wrong: %+v", updated)
	}
}

func TestDeleteTaskTwice(t *testing.T) {
	svc, _ := newService(t, fakeGroups{})
	ctx := context.Background()
	created, _, err := svc.CreateTask(ctx, 1, model.CreateInput{Title: "t", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteTask(ctx, 1, created.ID); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := svc.DeleteTask(ctx, 1, created.ID); err != nil {
		t.Fatalf("second delete must also succeed: %v", err)
	}
	_, err = svc.GetTask(ctx, 1, created.ID)
	requireKind(t, err, apperr.KindNotFound)
}

func TestGroupTasksHiddenFromNonMembers(t *testing.T) {
	const groupID, member, outsider = 10, 1, 2
	svc, _ := newService(t, fakeGroups{groupID: {member: model.RoleMember}})
	ctx := context.Background()

	gid := int64(groupID)
	created, _, err := svc.CreateTask(ctx, member, model.CreateInput{Title: "shared", GroupID: &gid, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.ListTasks(ctx, member, model.ListFilter{Scope: model.ScopeGroup, GroupID: groupID})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tasks) != 1 {
		t.Fatalf("member sees %d tasks, want 1", len(res.Tasks))
	}

	_, err = svc.ListTasks(ctx, outsider, model.ListFilter{Scope: model.ScopeGroup, GroupID: groupID})
	requireKind(t, err, apperr.KindPermissionDenied)
	_, err = svc.GetTask(ctx, outsider, created.ID)
	requireKind(t, err, apperr.KindPermissionDenied)
	_, _, err = svc.CreateTask(ctx, outsider, model.CreateInput{Title: "x", GroupID: &gid, IdempotencyKey: uuid.NewString()})
	requireKind(t, err, apperr.KindPermissionDenied)
}

func TestListTasksKeysetPagination(t *testing.T) {
	svc, _ := newService(t, fakeGroups{})
	ctx := context.Background()
	for i := range 5 {
		_, _, err := svc.CreateTask(ctx, 1, model.CreateInput{Title: "t", Priority: int32(i % 4), IdempotencyKey: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
	}

	var seen []model.Task
	f := model.ListFilter{Scope: model.ScopePersonal, PageSize: 2}
	for {
		res, err := svc.ListTasks(ctx, 1, f)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, res.Tasks...)
		if res.NextCursor == "" {
			break
		}
		f.Cursor = res.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("paged through %d tasks, want 5", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		prev, cur := seen[i-1], seen[i]
		if prev.Priority < cur.Priority || (prev.Priority == cur.Priority && prev.ID < cur.ID) {
			t.Fatalf("wrong order at %d: %+v then %+v", i, prev, cur)
		}
	}
}
