package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc"

	taskv1 "highload-sis2/gen/tasktracker/v1"
	"highload-sis2/internal/client/groupclient"
	"highload-sis2/internal/repository/cache"
	taskrepo "highload-sis2/internal/repository/task"
	"highload-sis2/internal/service/task"
	"highload-sis2/internal/transport/grpcserver"
	"highload-sis2/internal/transport/httpserver"
	"highload-sis2/migrations"
)

type config struct {
	httpAddr, grpcAddr     string
	primaryDSN, replicaDSN string
	redisAddr              string
	groupAddr              string
	cacheEnabled           bool
	dbMaxConns             int32
	secret                 []byte
}

func loadConfig() (config, error) {
	c := config{
		httpAddr:   envOr("HTTP_ADDR", ":8080"),
		grpcAddr:   envOr("GRPC_ADDR", ":9090"),
		primaryDSN: os.Getenv("PRIMARY_DSN"),
		replicaDSN: os.Getenv("REPLICA_DSN"),
		redisAddr:  envOr("REDIS_ADDR", "localhost:6379"),
		groupAddr:  envOr("GROUP_SERVICE_ADDR", "localhost:9091"),
		secret:     []byte(os.Getenv("JWT_SECRET")),
	}
	if len(c.secret) == 0 {
		return c, errors.New("JWT_SECRET is not set")
	}
	if c.primaryDSN == "" {
		return c, errors.New("PRIMARY_DSN is not set")
	}
	if c.replicaDSN == "" {
		c.replicaDSN = c.primaryDSN
	}
	enabled, err := strconv.ParseBool(envOr("CACHE_ENABLED", "true"))
	if err != nil {
		return c, fmt.Errorf("CACHE_ENABLED: %w", err)
	}
	c.cacheEnabled = enabled
	maxConns, err := strconv.Atoi(envOr("DB_MAX_CONNS", "32"))
	if err != nil {
		return c, fmt.Errorf("DB_MAX_CONNS: %w", err)
	}
	c.dbMaxConns = int32(maxConns)
	return c, nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("task-service stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := migrations.Up(ctx, cfg.primaryDSN); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	primary, err := taskrepo.NewPool(ctx, cfg.primaryDSN, cfg.dbMaxConns)
	if err != nil {
		return fmt.Errorf("primary pool: %w", err)
	}
	defer primary.Close()
	// MOCK: in docker-compose REPLICA_DSN is the same as PRIMARY_DSN.
	replica, err := taskrepo.NewPool(ctx, cfg.replicaDSN, cfg.dbMaxConns)
	if err != nil {
		return fmt.Errorf("replica pool: %w", err)
	}
	defer replica.Close()

	rdb := cache.NewRedis(cfg.redisAddr)
	defer rdb.Close()

	groups, err := groupclient.New(cfg.groupAddr)
	if err != nil {
		return err
	}
	defer groups.Close()
	readyCtx, cancelReady := context.WithTimeout(ctx, 3*time.Second)
	if err := groups.WaitReady(readyCtx); err != nil {

		log.Warn("group-service is not reachable yet", "err", err)
	}
	cancelReady()

	repo := taskrepo.NewRepo(primary, replica)
	taskCache := cache.New(rdb, repo, cfg.cacheEnabled, log)
	svc := task.NewService(repo, taskCache, taskCache, groups)

	grpcSrv := grpc.NewServer()
	taskv1.RegisterTaskServiceServer(grpcSrv, grpcserver.NewTaskServer(svc))
	// MOCK: there is no API Gateway; clients call task-service directly. In production a gateway
	// would sit in front of it: TLS, JWT verification, rate limiting, routing.
	httpSrv := httpserver.NewServer(cfg.httpAddr, httpserver.New(svc, cfg.secret, log))

	lis, err := net.Listen("tcp", cfg.grpcAddr)
	if err != nil {
		return err
	}

	errCh := make(chan error, 2)
	go func() { errCh <- grpcSrv.Serve(lis) }()
	go func() {
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	log.Info("task-service listening", "http", cfg.httpAddr, "grpc", cfg.grpcAddr, "cache_enabled", cfg.cacheEnabled)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	grpcSrv.GracefulStop()
	st := taskCache.Stats()
	log.Info("task-service stopped", "cache_hits", st.Hits, "cache_misses", st.Misses,
		"cache_invalidation_failures", st.InvalidationFailures)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
