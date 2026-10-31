package testinfra

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"highload-sis2/internal/repository/cache"
	taskrepo "highload-sis2/internal/repository/task"
	"highload-sis2/migrations"
)

func Postgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("tasks"),
		tcpostgres.WithUsername("tasks"),
		tcpostgres.WithPassword("tasks"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres dsn: %v", err)
	}
	if err := migrations.Up(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := taskrepo.NewPool(ctx, dsn, 8)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func Redis(t *testing.T) *redis.Client {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	ctr, err := tcredis.Run(ctx, "redis:7-alpine")
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	endpoint, err := ctr.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("redis endpoint: %v", err)
	}
	rdb := cache.NewRedis(endpoint)
	t.Cleanup(func() { _ = rdb.Close() })

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}
	return rdb
}
