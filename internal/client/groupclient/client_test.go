package groupclient_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	taskv1 "highload-sis2/gen/tasktracker/v1"
	"highload-sis2/internal/apperr"
	"highload-sis2/internal/client/groupclient"
	"highload-sis2/internal/model"
	grouprepo "highload-sis2/internal/repository/group"
	"highload-sis2/internal/service/group"
	"highload-sis2/internal/service/task"
	"highload-sis2/internal/transport/grpcserver"
)

type countingServer struct {
	*grpcserver.GroupServer
	calls *atomic.Int64
}

func (s countingServer) CheckMembership(ctx context.Context, req *taskv1.CheckMembershipRequest) (*taskv1.CheckMembershipResponse, error) {
	s.calls.Add(1)
	return s.GroupServer.CheckMembership(ctx, req)
}

func startGroupService(t *testing.T, fault grpcserver.FaultConfig) (*groupclient.Client, *group.Service, *atomic.Int64) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	var attempts atomic.Int64
	svc := group.NewService(grouprepo.NewMemoryRepository(), "http://test/invite/")
	srv := grpc.NewServer()
	taskv1.RegisterGroupServiceServer(srv, countingServer{grpcserver.NewGroupServer(svc, fault), &attempts})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, err := groupclient.New("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	return client, svc, &attempts
}

func sequence(vals ...float64) func() float64 {
	var i atomic.Int64
	return func() float64 {
		n := int(i.Add(1)) - 1
		if n < len(vals) {
			return vals[n]
		}
		return 1
	}
}

func TestCheckMembershipRetriesUnavailable(t *testing.T) {
	client, svc, attempts := startGroupService(t, grpcserver.FaultConfig{UnavailableRate: 0.5, Rand: sequence(0, 0.9)})
	const owner = 1
	g, _, _, err := svc.CreateGroup(context.Background(), owner, group.CreateInput{Name: "team"})
	if err != nil {
		t.Fatal(err)
	}

	role, err := client.CheckMembership(context.Background(), g.ID, owner)
	if err != nil {
		t.Fatalf("expected success after retry, got %v (attempts=%d)", err, attempts.Load())
	}
	if role != model.RoleOwner {
		t.Fatalf("role = %v, want owner", role)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	t.Logf("CheckMembership succeeded after %d attempts", attempts.Load())
}

func TestCheckMembershipRetryBudgetLimitedByDeadline(t *testing.T) {
	client, _, attempts := startGroupService(t, grpcserver.FaultConfig{UnavailableRate: 1})
	start := time.Now()
	_, err := client.CheckMembership(context.Background(), 1, 1)
	elapsed := time.Since(start)

	kind := apperr.KindOf(err)
	if kind != apperr.KindDeadline && kind != apperr.KindUnavailable {
		t.Fatalf("want DEADLINE_EXCEEDED or UNAVAILABLE, got %v", err)
	}
	if got := attempts.Load(); got < 2 || got > 3 {
		t.Fatalf("attempts = %d, want 2..3", got)
	}
	if elapsed > 75*time.Millisecond {
		t.Fatalf("retries ran %v, past the 50ms deadline", elapsed)
	}
	t.Logf("gave up after %d attempts in %v: %v", attempts.Load(), elapsed, err)
}

func TestRetryPolicyMaxAttempts(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	var calls atomic.Int64
	srv := grpc.NewServer()
	inner := grpcserver.NewGroupServer(group.NewService(grouprepo.NewMemoryRepository(), ""), grpcserver.FaultConfig{UnavailableRate: 1})
	taskv1.RegisterGroupServiceServer(srv, countingServer{inner, &calls})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(groupclient.RetryServiceConfig),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = taskv1.NewGroupServiceClient(conn).CheckMembership(ctx, &taskv1.CheckMembershipRequest{GroupId: 1, UserId: 1})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want UNAVAILABLE, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("attempts = %d, want maxAttempts=3", got)
	}
}

func TestCheckMembershipRespectsIncomingDeadline(t *testing.T) {
	client, _, attempts := startGroupService(t, grpcserver.FaultConfig{Delay: 80 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.CheckMembership(ctx, 1, 1)
	elapsed := time.Since(start)

	if apperr.KindOf(err) != apperr.KindDeadline {
		t.Fatalf("want DEADLINE_EXCEEDED, got %v", err)
	}
	if elapsed >= groupclient.MembershipTimeout {
		t.Fatalf("call took %v: incoming 30ms deadline was not propagated", elapsed)
	}

	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry on deadline)", got)
	}
}

func TestCheckMembershipOwnDeadline(t *testing.T) {
	client, _, _ := startGroupService(t, grpcserver.FaultConfig{Delay: 80 * time.Millisecond})
	start := time.Now()
	_, err := client.CheckMembership(context.Background(), 1, 1)
	if apperr.KindOf(err) != apperr.KindDeadline {
		t.Fatalf("want DEADLINE_EXCEEDED, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 75*time.Millisecond {
		t.Fatalf("call took %v, want about %v", elapsed, groupclient.MembershipTimeout)
	}
}

type countingStore struct{ calls atomic.Int64 }

func (s *countingStore) Create(context.Context, int64, uuid.UUID, model.CreateInput, []byte) (model.Task, bool, error) {
	s.calls.Add(1)
	return model.Task{}, true, nil
}

func (s *countingStore) GetForWrite(context.Context, int64) (model.Task, bool, error) {
	s.calls.Add(1)
	return model.Task{}, false, nil
}

func (s *countingStore) Update(context.Context, model.UpdateInput) (model.Task, error) {
	s.calls.Add(1)
	return model.Task{}, nil
}

func (s *countingStore) SoftDelete(context.Context, int64) error {
	s.calls.Add(1)
	return nil
}

type noopInvalidator struct{}

func (noopInvalidator) AfterWrite(context.Context, int64, *int64) {}

func TestCreateGroupTaskDeadlineSkipsDatabase(t *testing.T) {
	client, _, _ := startGroupService(t, grpcserver.FaultConfig{Delay: 80 * time.Millisecond})
	store := &countingStore{}
	svc := task.NewService(store, nil, noopInvalidator{}, client)

	gid := int64(1)
	_, _, err := svc.CreateTask(context.Background(), 1, model.CreateInput{
		Title: "t", GroupID: &gid, IdempotencyKey: uuid.NewString(),
	})
	if apperr.KindOf(err) != apperr.KindDeadline {
		t.Fatalf("want DEADLINE_EXCEEDED, got %v", err)
	}
	if n := store.calls.Load(); n != 0 {
		t.Fatalf("database was called %d times, want 0", n)
	}
}
