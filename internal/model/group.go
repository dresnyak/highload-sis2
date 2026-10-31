package model

import "time"

type Role int

const (
	RoleMember Role = iota + 1
	RoleOwner
)

type Group struct {
	ID          int64
	OwnerID     int64
	Name        string
	MaxMembers  int
	MemberCount int
	CreatedAt   time.Time
}

type Invite struct {
	GroupID   int64
	TokenHash string
	ExpiresAt time.Time
	MaxUses   int
	UsesCount int
	RevokedAt *time.Time
}
