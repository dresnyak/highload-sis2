package group

import (
	"context"
	"sync"
	"time"

	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
)

type Repository interface {
	CreateGroup(ctx context.Context, g model.Group, inv model.Invite) (model.Group, error)
	GetInvite(ctx context.Context, tokenHash string) (model.Invite, model.Group, error)
	Join(ctx context.Context, tokenHash string, userID int64, now time.Time) (model.Group, bool, error)
	Membership(ctx context.Context, groupID, userID int64) (role model.Role, banned bool, err error)
	RemoveMember(ctx context.Context, groupID, actorID, userID int64, ban bool) error
	TransferOwnership(ctx context.Context, groupID, actorID, newOwnerID int64) (model.Group, error)
	DeleteGroup(ctx context.Context, groupID, actorID int64) error
}

type groupRecord struct {
	group   model.Group
	members map[int64]model.Role
	banned  map[int64]bool
}

// MemoryRepository — MOCK: data lives in process memory, is lost on restart and is
// not shared between group-service replicas. In production this is PostgreSQL with
// tables groups, group_members (group_id, user_id, role, joined_at),
// group_invites (token_hash UNIQUE, expires_at, max_uses, uses_count, revoked_at)
// and group_bans; the Repository interface stays the same.
type MemoryRepository struct {
	mu      sync.RWMutex
	nextID  int64
	groups  map[int64]*groupRecord
	invites map[string]*model.Invite
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		groups:  make(map[int64]*groupRecord),
		invites: make(map[string]*model.Invite),
	}
}

func (r *MemoryRepository) CreateGroup(_ context.Context, g model.Group, inv model.Invite) (model.Group, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	g.ID = r.nextID
	g.MemberCount = 1
	r.groups[g.ID] = &groupRecord{
		group:   g,
		members: map[int64]model.Role{g.OwnerID: model.RoleOwner},
		banned:  make(map[int64]bool),
	}
	inv.GroupID = g.ID
	r.invites[inv.TokenHash] = &inv
	return g, nil
}

func (r *MemoryRepository) GetInvite(_ context.Context, tokenHash string) (model.Invite, model.Group, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	inv, ok := r.invites[tokenHash]
	if !ok {
		return model.Invite{}, model.Group{}, apperr.New(apperr.KindNotFound, "invite not found")
	}
	rec, ok := r.groups[inv.GroupID]
	if !ok {
		return model.Invite{}, model.Group{}, apperr.New(apperr.KindNotFound, "invite not found")
	}
	return *inv, rec.group, nil
}

func (r *MemoryRepository) Join(_ context.Context, tokenHash string, userID int64, now time.Time) (model.Group, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	inv, ok := r.invites[tokenHash]
	if !ok {
		return model.Group{}, false, apperr.New(apperr.KindNotFound, "invite not found")
	}
	rec, ok := r.groups[inv.GroupID]
	if !ok {
		return model.Group{}, false, apperr.New(apperr.KindNotFound, "invite not found")
	}
	if _, member := rec.members[userID]; member {
		return rec.group, true, nil
	}
	if rec.banned[userID] {
		return model.Group{}, false, apperr.New(apperr.KindPermissionDenied, "user is banned in this group")
	}
	if inv.RevokedAt != nil || !now.Before(inv.ExpiresAt) {
		return model.Group{}, false, apperr.New(apperr.KindNotFound, "invite expired or revoked")
	}
	if inv.MaxUses > 0 && inv.UsesCount >= inv.MaxUses {
		return model.Group{}, false, apperr.New(apperr.KindRateLimited, "invite usage limit reached")
	}
	if rec.group.MemberCount >= rec.group.MaxMembers {
		return model.Group{}, false, apperr.New(apperr.KindGroupFull, "group is full")
	}
	rec.members[userID] = model.RoleMember
	rec.group.MemberCount++
	inv.UsesCount++
	return rec.group, false, nil
}

func (r *MemoryRepository) Membership(_ context.Context, groupID, userID int64) (model.Role, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, ok := r.groups[groupID]
	if !ok {
		return 0, false, nil
	}
	return rec.members[userID], rec.banned[userID], nil
}

func (r *MemoryRepository) RemoveMember(_ context.Context, groupID, actorID, userID int64, ban bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, err := r.ownedGroup(groupID, actorID)
	if err != nil {
		return err
	}
	if userID == rec.group.OwnerID {
		return apperr.New(apperr.KindInvalid, "owner cannot be removed; transfer ownership first")
	}
	if _, ok := rec.members[userID]; ok {
		delete(rec.members, userID)
		rec.group.MemberCount--
	}
	if ban {
		rec.banned[userID] = true
	}
	return nil
}

func (r *MemoryRepository) TransferOwnership(_ context.Context, groupID, actorID, newOwnerID int64) (model.Group, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, err := r.ownedGroup(groupID, actorID)
	if err != nil {
		return model.Group{}, err
	}
	if _, ok := rec.members[newOwnerID]; !ok {
		return model.Group{}, apperr.New(apperr.KindInvalid, "new owner must be a group member")
	}
	rec.members[actorID] = model.RoleMember
	rec.members[newOwnerID] = model.RoleOwner
	rec.group.OwnerID = newOwnerID
	return rec.group, nil
}

func (r *MemoryRepository) DeleteGroup(_ context.Context, groupID, actorID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, err := r.ownedGroup(groupID, actorID); err != nil {
		return err
	}
	delete(r.groups, groupID)
	for h, inv := range r.invites {
		if inv.GroupID == groupID {
			delete(r.invites, h)
		}
	}
	return nil
}

func (r *MemoryRepository) ownedGroup(groupID, actorID int64) (*groupRecord, error) {
	rec, ok := r.groups[groupID]
	if !ok {
		return nil, apperr.New(apperr.KindNotFound, "group %d not found", groupID)
	}
	if rec.group.OwnerID != actorID {
		return nil, apperr.New(apperr.KindPermissionDenied, "only the group owner can do this")
	}
	return rec, nil
}
