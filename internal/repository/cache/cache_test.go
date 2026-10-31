package cache_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"highload-sis2/internal/model"
	"highload-sis2/internal/repository/cache"
	taskrepo "highload-sis2/internal/repository/task"
	"highload-sis2/internal/service/task"
	"highload-sis2/internal/testinfra"
)

type noGroups struct{}

func (noGroups) CheckMembership(context.Context, int64, int64) (model.Role, error) {
	return model.RoleMember, nil
}

func TestReadThroughAndInvalidation(t *testing.T) {
	pool := testinfra.Postgres(t)
	rdb := testinfra.Redis(t)
	repo := taskrepo.NewRepo(pool, pool)
	c := cache.New(rdb, repo, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc := task.NewService(repo, c, c, noGroups{})
	ctx := context.Background()
	const user = 1

	created, _, err := svc.CreateTask(ctx, user, model.CreateInput{Title: "cached", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}

	step := func(name string, wantHits, wantMisses int64) {
		t.Helper()
		if _, err := svc.GetTask(ctx, user, created.ID); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		st := c.Stats()
		if st.Hits != wantHits || st.Misses != wantMisses {
			t.Fatalf("%s: hits=%d misses=%d, want hits=%d misses=%d", name, st.Hits, st.Misses, wantHits, wantMisses)
		}
	}

	step("first read is a miss and loads from db", 0, 1)
	step("second read is a hit", 1, 1)

	verBefore, _ := rdb.Get(ctx, "u:1:ver").Int64()
	title := "renamed"
	if _, err := svc.UpdateTask(ctx, user, model.UpdateInput{ID: created.ID, ExpectedVersion: 1, Title: &title}); err != nil {
		t.Fatal(err)
	}
	verAfter, _ := rdb.Get(ctx, "u:1:ver").Int64()
	if verAfter != verBefore+1 {
		t.Fatalf("owner version %d -> %d, want +1", verBefore, verAfter)
	}
	if ttl := rdb.TTL(ctx, "u:1:rw").Val(); ttl <= 0 {
		t.Fatalf("read-your-writes flag is not set (ttl=%v)", ttl)
	}

	step("read after write is a miss", 1, 2)
	got, err := svc.GetTask(ctx, user, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "renamed" || got.Version != 2 {
		t.Fatalf("stale value after write: %+v", got)
	}
}

func TestListFirstPageCached(t *testing.T) {
	pool := testinfra.Postgres(t)
	rdb := testinfra.Redis(t)
	repo := taskrepo.NewRepo(pool, pool)
	c := cache.New(rdb, repo, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc := task.NewService(repo, c, c, noGroups{})
	ctx := context.Background()

	if _, _, err := svc.CreateTask(ctx, 1, model.CreateInput{Title: "a", IdempotencyKey: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	f := model.ListFilter{Scope: model.ScopePersonal}
	for range 2 {
		if _, err := svc.ListTasks(ctx, 1, f); err != nil {
			t.Fatal(err)
		}
	}
	if st := c.Stats(); st.Hits != 1 || st.Misses != 1 {
		t.Fatalf("hits=%d misses=%d, want 1/1", st.Hits, st.Misses)
	}

	if _, _, err := svc.CreateTask(ctx, 1, model.CreateInput{Title: "b", IdempotencyKey: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.ListTasks(ctx, 1, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tasks) != 2 {
		t.Fatalf("list after create has %d tasks, want 2 (stale cache?)", len(res.Tasks))
	}
}

func TestRedisDownFallsBackToDatabase(t *testing.T) {
	pool := testinfra.Postgres(t)
	repo := taskrepo.NewRepo(pool, pool)

	c := cache.New(cache.NewRedis("127.0.0.1:1"), repo, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc := task.NewService(repo, c, c, noGroups{})
	ctx := context.Background()

	created, _, err := svc.CreateTask(ctx, 1, model.CreateInput{Title: "x", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatalf("write must not fail when redis is down: %v", err)
	}
	if _, err := svc.GetTask(ctx, 1, created.ID); err != nil {
		t.Fatalf("read must fall back to db when redis is down: %v", err)
	}
	if c.Stats().InvalidationFailures != 1 {
		t.Fatalf("invalidation failure was not counted")
	}
}
