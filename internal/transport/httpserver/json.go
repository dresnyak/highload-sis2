package httpserver

import (
	"time"

	"highload-sis2/internal/model"
)

type taskJSON struct {
	ID          int64     `json:"id"`
	AuthorID    int64     `json:"author_id"`
	GroupID     *int64    `json:"group_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Priority    int32     `json:"priority"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type createTaskJSON struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	GroupID     *int64 `json:"group_id"`
	Priority    int32  `json:"priority"`
}

type updateTaskJSON struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
	Priority    *int32  `json:"priority"`
}

type listJSON struct {
	Tasks      []taskJSON `json:"tasks"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

func toJSON(t model.Task) taskJSON {
	return taskJSON{
		ID:          t.ID,
		AuthorID:    t.AuthorID,
		GroupID:     t.GroupID,
		Title:       t.Title,
		Description: t.Description,
		Status:      string(t.Status),
		Priority:    t.Priority,
		Version:     t.Version,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}
