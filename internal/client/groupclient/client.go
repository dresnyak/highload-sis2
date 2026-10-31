package groupclient

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	taskv1 "highload-sis2/gen/tasktracker/v1"
	"highload-sis2/internal/apperr"
	"highload-sis2/internal/model"
)

const MembershipTimeout = 50 * time.Millisecond

type Client struct {
	conn *grpc.ClientConn
	api  taskv1.GroupServiceClient
}

// MOCK: no TLS, the services share one docker network; production would use mTLS.
func New(addr string, opts ...grpc.DialOption) (*Client, error) {
	opts = append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(RetryServiceConfig),
	}, opts...)
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial group-service: %w", err)
	}
	return &Client{conn: conn, api: taskv1.NewGroupServiceClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) WaitReady(ctx context.Context) error {
	c.conn.Connect()
	for {
		s := c.conn.GetState()
		if s == connectivity.Ready {
			return nil
		}
		if !c.conn.WaitForStateChange(ctx, s) {
			return fmt.Errorf("group-service not ready: %w", ctx.Err())
		}
	}
}

func (c *Client) CheckMembership(ctx context.Context, groupID, userID int64) (model.Role, error) {
	ctx, cancel := context.WithTimeout(ctx, MembershipTimeout)
	defer cancel()
	resp, err := c.api.CheckMembership(metadata.AppendToOutgoingContext(ctx, "x-user-id", strconv.FormatInt(userID, 10)),
		&taskv1.CheckMembershipRequest{GroupId: groupID, UserId: userID})
	if err != nil {
		return 0, apperr.FromGRPC(err, "group-service CheckMembership")
	}
	if !resp.GetIsMember() {
		return 0, apperr.New(apperr.KindPermissionDenied, "user %d is not a member of group %d", userID, groupID)
	}
	if resp.GetRole() == taskv1.GroupRole_GROUP_ROLE_OWNER {
		return model.RoleOwner, nil
	}
	return model.RoleMember, nil
}
