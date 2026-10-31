package model

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"highload-sis2/internal/apperr"
)

type Cursor struct {
	Sort      SortBy `json:"s"`
	Priority  int32  `json:"p,omitempty"`
	CreatedAt int64  `json:"c,omitempty"`
	ID        int64  `json:"id"`
}

func EncodeCursor(sort SortBy, t Task) string {
	c := Cursor{Sort: sort, ID: t.ID}
	if sort == SortByPriority {
		c.Priority = t.Priority
	} else {
		c.CreatedAt = t.CreatedAt.UnixMicro()
	}
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func DecodeCursor(s string) (Cursor, error) {
	var c Cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, apperr.New(apperr.KindInvalid, "malformed page token")
	}
	if err := json.Unmarshal(b, &c); err != nil || c.ID <= 0 {
		return c, apperr.New(apperr.KindInvalid, "malformed page token")
	}
	return c, nil
}

func (c Cursor) CreatedAtTime() time.Time { return time.UnixMicro(c.CreatedAt) }
