package group

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
	grouprepo "highload-sis2/internal/repository/group"
)

const (
	defaultMaxMembers = 100
	defaultInviteTTL  = 7 * 24 * time.Hour
	maxNameLen        = 200
)

type Service struct {
	repo          grouprepo.Repository
	inviteBaseURL string
	now           func() time.Time
}

func NewService(r grouprepo.Repository, inviteBaseURL string) *Service {
	return &Service{repo: r, inviteBaseURL: inviteBaseURL, now: time.Now}
}

type CreateInput struct {
	Name          string
	MaxMembers    int
	InviteMaxUses int
	InviteTTL     time.Duration
}

func (s *Service) CreateGroup(ctx context.Context, ownerID int64, in CreateInput) (g model.Group, token, url string, err error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return model.Group{}, "", "", apperr.New(apperr.KindInvalid, "name must be 1..200 characters")
	}
	if in.MaxMembers < 0 || in.InviteMaxUses < 0 || in.InviteTTL < 0 {
		return model.Group{}, "", "", apperr.New(apperr.KindInvalid, "limits must not be negative")
	}
	if in.MaxMembers == 0 {
		in.MaxMembers = defaultMaxMembers
	}
	if in.InviteTTL == 0 {
		in.InviteTTL = defaultInviteTTL
	}

	token, err = newToken()
	if err != nil {
		return model.Group{}, "", "", err
	}
	now := s.now()
	g, err = s.repo.CreateGroup(ctx,
		model.Group{OwnerID: ownerID, Name: name, MaxMembers: in.MaxMembers, CreatedAt: now},
		model.Invite{TokenHash: HashToken(token), ExpiresAt: now.Add(in.InviteTTL), MaxUses: in.InviteMaxUses})
	if err != nil {
		return model.Group{}, "", "", err
	}
	return g, token, s.inviteBaseURL + token, nil
}

func (s *Service) Preview(ctx context.Context, token string) (model.Group, model.Invite, error) {
	if token == "" {
		return model.Group{}, model.Invite{}, apperr.New(apperr.KindInvalid, "token is required")
	}
	inv, g, err := s.repo.GetInvite(ctx, HashToken(token))
	if err != nil {
		return model.Group{}, model.Invite{}, err
	}
	if inv.RevokedAt != nil || !s.now().Before(inv.ExpiresAt) {
		return model.Group{}, model.Invite{}, apperr.New(apperr.KindNotFound, "invite expired or revoked")
	}
	return g, inv, nil
}

func (s *Service) Join(ctx context.Context, userID int64, token string) (model.Group, bool, error) {
	if token == "" {
		return model.Group{}, false, apperr.New(apperr.KindInvalid, "token is required")
	}
	return s.repo.Join(ctx, HashToken(token), userID, s.now())
}

func (s *Service) RemoveMember(ctx context.Context, actorID, groupID, userID int64, ban bool) error {
	return s.repo.RemoveMember(ctx, groupID, actorID, userID, ban)
}

func (s *Service) TransferOwnership(ctx context.Context, actorID, groupID, newOwnerID int64) (model.Group, error) {
	return s.repo.TransferOwnership(ctx, groupID, actorID, newOwnerID)
}

func (s *Service) DeleteGroup(ctx context.Context, actorID, groupID int64) error {
	return s.repo.DeleteGroup(ctx, groupID, actorID)
}

func (s *Service) CheckMembership(ctx context.Context, groupID, userID int64) (model.Role, bool, error) {
	return s.repo.Membership(ctx, groupID, userID)
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate invite token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
