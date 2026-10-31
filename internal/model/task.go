package model

import (
	"crypto/sha256"
	"encoding/binary"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"highload-sis2/internal/apperr"
)

type Status string

const (
	StatusPlanned    Status = "planned"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)

func (s Status) Valid() bool {
	return s == StatusPlanned || s == StatusInProgress || s == StatusCompleted
}

type Task struct {
	ID          int64
	AuthorID    int64
	GroupID     *int64
	Title       string
	Description string
	Status      Status
	Priority    int32
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CreateInput struct {
	Title          string
	Description    string
	GroupID        *int64
	Priority       int32
	IdempotencyKey string
}

type UpdateInput struct {
	ID              int64
	ExpectedVersion int64
	Title           *string
	Description     *string
	Status          *Status
	Priority        *int32
}

type Scope int

const (
	ScopePersonal Scope = iota + 1
	ScopeGroup
)

type SortBy int

const (
	SortByPriority SortBy = iota + 1
	SortByCreatedAt
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 100
	maxTitleLen     = 500
)

type ListFilter struct {
	Scope    Scope
	AuthorID int64
	GroupID  int64
	Statuses []Status
	SortBy   SortBy
	PageSize int
	Cursor   string
}

type ListResult struct {
	Tasks      []Task
	NextCursor string
}

func validatePriority(p int32) error {
	if p < 0 || p > 3 {
		return apperr.New(apperr.KindInvalid, "priority must be in 0..3")
	}
	return nil
}

func validateTitle(t string) error {
	if t == "" {
		return apperr.New(apperr.KindInvalid, "title is required")
	}

	if utf8.RuneCountInString(t) > maxTitleLen {
		return apperr.New(apperr.KindInvalid, "title is longer than 500 characters")
	}
	return nil
}

func (in CreateInput) Validate() (uuid.UUID, error) {
	key, err := uuid.Parse(in.IdempotencyKey)
	if err != nil {
		return uuid.UUID{}, apperr.New(apperr.KindInvalid, "idempotency key must be a UUID")
	}
	if err := validateTitle(in.Title); err != nil {
		return uuid.UUID{}, err
	}
	if err := validatePriority(in.Priority); err != nil {
		return uuid.UUID{}, err
	}
	return key, nil
}

func (in CreateInput) RequestHash() []byte {
	h := sha256.New()
	writeField := func(b []byte) {

		_ = binary.Write(h, binary.BigEndian, uint32(len(b)))
		h.Write(b)
	}
	writeField([]byte(in.Title))
	writeField([]byte(in.Description))
	var gid int64 = -1
	if in.GroupID != nil {
		gid = *in.GroupID
	}
	_ = binary.Write(h, binary.BigEndian, gid)
	_ = binary.Write(h, binary.BigEndian, in.Priority)
	return h.Sum(nil)
}

func (in UpdateInput) Validate() error {
	if in.ID <= 0 {
		return apperr.New(apperr.KindInvalid, "id is required")
	}
	if in.ExpectedVersion <= 0 {
		return apperr.New(apperr.KindInvalid, "expected version is required")
	}
	if in.Title == nil && in.Description == nil && in.Status == nil && in.Priority == nil {
		return apperr.New(apperr.KindInvalid, "nothing to update")
	}
	if in.Title != nil {
		if err := validateTitle(*in.Title); err != nil {
			return err
		}
	}
	if in.Status != nil && !in.Status.Valid() {
		return apperr.New(apperr.KindInvalid, "unknown status")
	}
	if in.Priority != nil {
		if err := validatePriority(*in.Priority); err != nil {
			return err
		}
	}
	return nil
}

func (f *ListFilter) Normalize() error {
	if f.SortBy == 0 {
		f.SortBy = SortByPriority
	}
	if f.SortBy != SortByPriority && f.SortBy != SortByCreatedAt {
		return apperr.New(apperr.KindInvalid, "unknown sort")
	}
	if f.PageSize == 0 {
		f.PageSize = DefaultPageSize
	}
	if f.PageSize < 0 || f.PageSize > MaxPageSize {
		return apperr.New(apperr.KindInvalid, "page size must be in 1..100")
	}
	for _, s := range f.Statuses {
		if !s.Valid() {
			return apperr.New(apperr.KindInvalid, "unknown status")
		}
	}
	if f.Cursor != "" {
		if _, err := DecodeCursor(f.Cursor); err != nil {
			return err
		}
	}
	return nil
}
