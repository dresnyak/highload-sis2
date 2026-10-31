package model

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "highload-sis2/gen/tasktracker/v1"
)

var statusToProto = map[Status]taskv1.TaskStatus{
	StatusPlanned:    taskv1.TaskStatus_TASK_STATUS_PLANNED,
	StatusInProgress: taskv1.TaskStatus_TASK_STATUS_IN_PROGRESS,
	StatusCompleted:  taskv1.TaskStatus_TASK_STATUS_COMPLETED,
}

func StatusToProto(s Status) taskv1.TaskStatus { return statusToProto[s] }

func StatusFromProto(s taskv1.TaskStatus) Status {
	for k, v := range statusToProto {
		if v == s {
			return k
		}
	}
	return ""
}

func TaskToProto(t Task) *taskv1.Task {
	return &taskv1.Task{
		Id:          t.ID,
		AuthorId:    t.AuthorID,
		GroupId:     t.GroupID,
		Title:       t.Title,
		Description: t.Description,
		Status:      StatusToProto(t.Status),
		Priority:    t.Priority,
		Version:     t.Version,
		CreatedAt:   timestamppb.New(t.CreatedAt),
		UpdatedAt:   timestamppb.New(t.UpdatedAt),
	}
}

func TaskFromProto(p *taskv1.Task) Task {
	return Task{
		ID:          p.GetId(),
		AuthorID:    p.GetAuthorId(),
		GroupID:     p.GroupId,
		Title:       p.GetTitle(),
		Description: p.GetDescription(),
		Status:      StatusFromProto(p.GetStatus()),
		Priority:    p.GetPriority(),
		Version:     p.GetVersion(),
		CreatedAt:   p.GetCreatedAt().AsTime(),
		UpdatedAt:   p.GetUpdatedAt().AsTime(),
	}
}

func ListToProto(r ListResult) *taskv1.ListTasksResponse {
	out := &taskv1.ListTasksResponse{
		Tasks:         make([]*taskv1.Task, len(r.Tasks)),
		NextPageToken: r.NextCursor,
	}
	for i, t := range r.Tasks {
		out.Tasks[i] = TaskToProto(t)
	}
	return out
}

func ListFromProto(p *taskv1.ListTasksResponse) ListResult {
	out := ListResult{Tasks: make([]Task, len(p.GetTasks())), NextCursor: p.GetNextPageToken()}
	for i, t := range p.GetTasks() {
		out.Tasks[i] = TaskFromProto(t)
	}
	return out
}
