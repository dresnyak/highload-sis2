package grpcserver

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "highload-sis2/gen/tasktracker/v1"
	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
	"highload-sis2/internal/service/group"
)

type GroupServer struct {
	taskv1.UnimplementedGroupServiceServer
	svc   *group.Service
	fault FaultConfig
}

func NewGroupServer(svc *group.Service, fault FaultConfig) *GroupServer {
	return &GroupServer{svc: svc, fault: fault}
}

func (s *GroupServer) CreateGroup(ctx context.Context, req *taskv1.CreateGroupRequest) (*taskv1.CreateGroupResponse, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	g, token, url, err := s.svc.CreateGroup(ctx, userID, group.CreateInput{
		Name:          req.GetName(),
		MaxMembers:    int(req.GetMaxMembers()),
		InviteMaxUses: int(req.GetInviteMaxUses()),
		InviteTTL:     time.Duration(req.GetInviteTtlSeconds()) * time.Second,
	})
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &taskv1.CreateGroupResponse{Group: groupToProto(g), InviteUrl: url, InviteToken: token}, nil
}

func (s *GroupServer) GetInvitePreview(ctx context.Context, req *taskv1.InviteTokenRequest) (*taskv1.GroupPreview, error) {
	g, inv, err := s.svc.Preview(ctx, req.GetToken())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &taskv1.GroupPreview{
		GroupId:     g.ID,
		Name:        g.Name,
		MemberCount: int32(g.MemberCount),
		ExpiresAt:   timestamppb.New(inv.ExpiresAt),
	}, nil
}

func (s *GroupServer) JoinByInvite(ctx context.Context, req *taskv1.InviteTokenRequest) (*taskv1.JoinResponse, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	g, already, err := s.svc.Join(ctx, userID, req.GetToken())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &taskv1.JoinResponse{Group: groupToProto(g), AlreadyMember: already}, nil
}

func (s *GroupServer) RemoveMember(ctx context.Context, req *taskv1.RemoveMemberRequest) (*emptypb.Empty, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.svc.RemoveMember(ctx, userID, req.GetGroupId(), req.GetUserId(), req.GetBan()); err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *GroupServer) TransferOwnership(ctx context.Context, req *taskv1.TransferOwnershipRequest) (*taskv1.Group, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	g, err := s.svc.TransferOwnership(ctx, userID, req.GetGroupId(), req.GetNewOwnerId())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return groupToProto(g), nil
}

func (s *GroupServer) DeleteGroup(ctx context.Context, req *taskv1.DeleteGroupRequest) (*emptypb.Empty, error) {
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.svc.DeleteGroup(ctx, userID, req.GetGroupId()); err != nil {
		return nil, apperr.ToGRPC(err)
	}
	return &emptypb.Empty{}, nil
}

// MOCK: in production this call would use a service identity (mTLS / service token).
func (s *GroupServer) CheckMembership(ctx context.Context, req *taskv1.CheckMembershipRequest) (*taskv1.CheckMembershipResponse, error) {
	if err := s.fault.inject(ctx); err != nil {
		return nil, err
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetUserId() != userID {
		return nil, apperr.ToGRPC(apperr.New(apperr.KindPermissionDenied, "can only check own membership"))
	}
	role, banned, err := s.svc.CheckMembership(ctx, req.GetGroupId(), req.GetUserId())
	if err != nil {
		return nil, apperr.ToGRPC(err)
	}
	resp := &taskv1.CheckMembershipResponse{IsMember: role != 0, Banned: banned}
	switch role {
	case model.RoleOwner:
		resp.Role = taskv1.GroupRole_GROUP_ROLE_OWNER
	case model.RoleMember:
		resp.Role = taskv1.GroupRole_GROUP_ROLE_MEMBER
	}
	return resp, nil
}

func groupToProto(g model.Group) *taskv1.Group {
	return &taskv1.Group{
		Id:          g.ID,
		OwnerId:     g.OwnerID,
		Name:        g.Name,
		MemberCount: int32(g.MemberCount),
		MaxMembers:  int32(g.MaxMembers),
		CreatedAt:   timestamppb.New(g.CreatedAt),
	}
}
