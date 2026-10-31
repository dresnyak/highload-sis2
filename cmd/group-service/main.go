package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc"

	taskv1 "highload-sis2/gen/tasktracker/v1"
	grouprepo "highload-sis2/internal/repository/group"
	"highload-sis2/internal/service/group"
	"highload-sis2/internal/transport/grpcserver"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("group-service stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	addr := envOr("GROUP_GRPC_ADDR", ":9091")
	baseURL := envOr("INVITE_BASE_URL", "http://localhost:8080/v1/invite-links/")

	fault, err := faultFromEnv()
	if err != nil {
		return err
	}

	svc := group.NewService(grouprepo.NewMemoryRepository(), baseURL)
	srv := grpc.NewServer()
	taskv1.RegisterGroupServiceServer(srv, grpcserver.NewGroupServer(svc, fault))

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	log.Info("group-service listening", "grpc", addr,
		"fault_unavailable_rate", fault.UnavailableRate, "fault_delay", fault.Delay.String())
	return srv.Serve(lis)
}

func faultFromEnv() (grpcserver.FaultConfig, error) {
	var cfg grpcserver.FaultConfig
	if v := os.Getenv("FAULT_UNAVAILABLE_RATE"); v != "" {
		rate, err := strconv.ParseFloat(v, 64)
		if err != nil || rate < 0 || rate > 1 {
			return cfg, errMissing("valid FAULT_UNAVAILABLE_RATE (0..1)")
		}
		cfg.UnavailableRate = rate
	}
	if v := os.Getenv("FAULT_DELAY_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 0 {
			return cfg, errMissing("valid FAULT_DELAY_MS")
		}
		cfg.Delay = time.Duration(ms) * time.Millisecond
	}
	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type errMissing string

func (e errMissing) Error() string { return string(e) + " is not set" }
