package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	taskrepo "highload-sis2/internal/repository/task"
	"highload-sis2/migrations"
)

func main() {
	users := flag.Int("users", 1000, "number of users")
	per := flag.Int("tasks-per-user", 100, "tasks per user")
	flag.Parse()

	dsn := os.Getenv("PRIMARY_DSN")
	if dsn == "" {
		dsn = "postgres://tasks:tasks@localhost:55432/tasks?sslmode=disable"
	}
	ctx := context.Background()
	if err := migrations.Up(ctx, dsn); err != nil {
		log.Fatal(err)
	}
	pool, err := taskrepo.NewPool(ctx, dsn, 2)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	start := time.Now()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET statement_timeout = 0`); err != nil {
		log.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `TRUNCATE tasks RESTART IDENTITY`); err != nil {
		log.Fatal(err)
	}

	tag, err := conn.Exec(ctx, `
		INSERT INTO tasks (author_id, title, description, status, priority, client_request_id, request_hash)
		SELECT u, 'Task ' || t || ' of user ' || u,
		       repeat('Lorem ipsum dolor sit amet. ', 4),
		       (ARRAY['planned', 'in_progress', 'completed'])[1 + t % 3],
		       t % 4, gen_random_uuid(), '\x00'::bytea
		FROM generate_series(1, $1::int) AS u, generate_series(1, $2::int) AS t
		ORDER BY u, t`, *users, *per)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `ANALYZE tasks`); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("seeded %d tasks for %d users in %v\n", tag.RowsAffected(), *users, time.Since(start).Round(time.Millisecond))
}
