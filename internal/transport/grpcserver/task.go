package grpcserver

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	taskv1 "highload-sis2/gen/tasktracker/v1"
	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
	"highload-sis2/internal/service/task"
)

type TaskServer struct {
	taskv1.UnimplementedTaskServiceServer
	svc *task.Service
}

func NewTaskServer(svc *task.Service) *TaskServer {
	return &TaskServer{svc: svc}
}

func (s *TaskServer) CreateTask(ctx context.Context, req *taskv1.CreateTaskRequest) (*taskv1.Task, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, _, err := s.svc.CreateTask(ctx, userID, model.CreateInput{
		Title:          req.GetTitle(),
		Description:    req.GetDescription(),
		GroupID:        req.GroupId,
		Priority:       req.GetPriority(),
		IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return model.TaskToProto(t), nil
}

func (s *TaskServer) GetTask(ctx context.Context, req *taskv1.GetTaskRequest) (*taskv1.Task, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.svc.GetTask(ctx, userID, req.GetId())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return model.TaskToProto(t), nil
}

func (s *TaskServer) ListTasks(ctx context.Context, req *taskv1.ListTasksRequest) (*taskv1.ListTasksResponse, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	f := model.ListFilter{
		GroupID:  req.GetGroupId(),
		PageSize: int(req.GetPageSize()),
		Cursor:   req.GetPageToken(),
	}
	switch req.GetScope() {
	case taskv1.Scope_SCOPE_PERSONAL:
		f.Scope = model.ScopePersonal
	case taskv1.Scope_SCOPE_GROUP:
		f.Scope = model.ScopeGroup
	}
	switch req.GetSortBy() {
	case taskv1.SortBy_SORT_BY_UNSPECIFIED, taskv1.SortBy_SORT_BY_PRIORITY:
		f.SortBy = model.SortByPriority
	case taskv1.SortBy_SORT_BY_CREATED_AT:
		f.SortBy = model.SortByCreatedAt
	}
	for _, st := range req.GetStatuses() {
		f.Statuses = append(f.Statuses, model.StatusFromProto(st))
	}
	res, err := s.svc.ListTasks(ctx, userID, f)
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return model.ListToProto(res), nil
}

func (s *TaskServer) UpdateTask(ctx context.Context, req *taskv1.UpdateTaskRequest) (*taskv1.Task, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	in := model.UpdateInput{
		ID:              req.GetId(),
		ExpectedVersion: req.GetExpectedVersion(),
		Title:           req.Title,
		Description:     req.Description,
		Priority:        req.Priority,
	}
	if req.Status != nil {
		st := model.StatusFromProto(req.GetStatus())
		in.Status = &st
	}
	t, err := s.svc.UpdateTask(ctx, userID, in)
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return model.TaskToProto(t), nil
}

func (s *TaskServer) DeleteTask(ctx context.Context, req *taskv1.DeleteTaskRequest) (*emptypb.Empty, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.svc.DeleteTask(ctx, userID, req.GetId()); err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &emptypb.Empty{}, nil
}
