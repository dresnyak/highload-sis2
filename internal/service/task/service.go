package task

import (
	"context"

	"github.com/google/uuid"

	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
)

type Store interface {
	Create(ctx context.Context, authorID int64, key uuid.UUID, in model.CreateInput, reqHash []byte) (model.Task, bool, error)
	GetForWrite(ctx context.Context, id int64) (model.Task, bool, error)
	Update(ctx context.Context, in model.UpdateInput) (model.Task, error)
	SoftDelete(ctx context.Context, id int64) error
}

type Reader interface {
	GetTask(ctx context.Context, viewerID, id int64) (model.Task, error)
	ListTasks(ctx context.Context, viewerID int64, f model.ListFilter) (model.ListResult, error)
}

type Invalidator interface {
	AfterWrite(ctx context.Context, userID int64, groupID *int64)
}

type Membership interface {
	CheckMembership(ctx context.Context, groupID, userID int64) (model.Role, error)
}

type Service struct {
	store  Store
	reader Reader
	inv    Invalidator
	groups Membership
}

func NewService(store Store, reader Reader, inv Invalidator, groups Membership) *Service {
	return &Service{store: store, reader: reader, inv: inv, groups: groups}
}

func (s *Service) CreateTask(ctx context.Context, userID int64, in model.CreateInput) (model.Task, bool, error) {
	key, err := in.Validate()
	if err != nil {
		return model.Task{}, false, err
	}
	if in.GroupID != nil {

		if _, err := s.groups.CheckMembership(ctx, *in.GroupID, userID); err != nil {
			return model.Task{}, false, err
		}
	}
	t, created, err := s.store.Create(ctx, userID, key, in, in.RequestHash())
	if err != nil {
		return model.Task{}, false, err
	}
	if created {
		s.inv.AfterWrite(ctx, userID, t.GroupID)
	}
	return t, created, nil
}

func (s *Service) GetTask(ctx context.Context, userID, id int64) (model.Task, error) {
	if id <= 0 {
		return model.Task{}, apperr.New(apperr.KindInvalid, "id is required")
	}
	t, err := s.reader.GetTask(ctx, userID, id)
	if err != nil {
		return model.Task{}, err
	}
	if err := s.checkRead(ctx, userID, t); err != nil {
		return model.Task{}, err
	}
	return t, nil
}

func (s *Service) ListTasks(ctx context.Context, userID int64, f model.ListFilter) (model.ListResult, error) {
	if err := f.Normalize(); err != nil {
		return model.ListResult{}, err
	}
	switch f.Scope {
	case model.ScopePersonal:
		f.AuthorID = userID
	case model.ScopeGroup:
		if f.GroupID <= 0 {
			return model.ListResult{}, apperr.New(apperr.KindInvalid, "group id is required for group scope")
		}

		if _, err := s.groups.CheckMembership(ctx, f.GroupID, userID); err != nil {
			return model.ListResult{}, err
		}
	default:
		return model.ListResult{}, apperr.New(apperr.KindInvalid, "scope is required")
	}
	return s.reader.ListTasks(ctx, userID, f)
}

func (s *Service) UpdateTask(ctx context.Context, userID int64, in model.UpdateInput) (model.Task, error) {
	if err := in.Validate(); err != nil {
		return model.Task{}, err
	}
	cur, deleted, err := s.store.GetForWrite(ctx, in.ID)
	if err != nil {
		return model.Task{}, err
	}
	if deleted {
		return model.Task{}, apperr.New(apperr.KindNotFound, "task %d not found", in.ID)
	}
	if err := s.checkRead(ctx, userID, cur); err != nil {
		return model.Task{}, err
	}
	t, err := s.store.Update(ctx, in)
	if err != nil {
		return model.Task{}, err
	}
	s.inv.AfterWrite(ctx, userID, t.GroupID)
	return t, nil
}

func (s *Service) DeleteTask(ctx context.Context, userID, id int64) error {
	if id <= 0 {
		return apperr.New(apperr.KindInvalid, "id is required")
	}
	cur, deleted, err := s.store.GetForWrite(ctx, id)
	if err != nil {
		return err
	}
	if cur.GroupID == nil {
		if cur.AuthorID != userID {
			return apperr.New(apperr.KindNotFound, "task %d not found", id)
		}
	} else if cur.AuthorID != userID {
		role, err := s.groups.CheckMembership(ctx, *cur.GroupID, userID)
		if err != nil {
			return err
		}
		if role != model.RoleOwner {
			return apperr.New(apperr.KindPermissionDenied, "only the author or the group owner can delete a group task")
		}
	}
	if deleted {
		return nil
	}
	if err := s.store.SoftDelete(ctx, id); err != nil {
		return err
	}
	s.inv.AfterWrite(ctx, userID, cur.GroupID)
	return nil
}

func (s *Service) checkRead(ctx context.Context, userID int64, t model.Task) error {
	if t.GroupID == nil {
		if t.AuthorID != userID {
			return apperr.New(apperr.KindNotFound, "task %d not found", t.ID)
		}
		return nil
	}
	_, err := s.groups.CheckMembership(ctx, *t.GroupID, userID)
	return err
}
