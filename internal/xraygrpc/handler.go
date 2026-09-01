package xraygrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	command "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/vless"
)

// pushTimeout bounds one live apply: a black-holed API address must fail the
// push — and surface the roster sync state — not stall the apply loop.
const pushTimeout = 4 * time.Second

// ErrUserExists marks xray's duplicate-email answer to an add. For the apply
// path that is success — the user is there — so AddUser returns nil for it
// and the sentinel exists for callers that need to distinguish.
var ErrUserExists = errors.New("user already exists in xray")

// ManagedUser is one roster user's live-push identity: the email that IS the
// identity, the Client ID credential, and the attach-time flow.
type ManagedUser struct {
	Email string
	ID    string
	Flow  string
}

// HandlerClient is the HandlerService client — the live half of the apply
// path (user-management spec §4). Like the stats Client it opens a fresh
// connection per call and relies on the loopback-only, no-auth security
// model; the xray api object must list HandlerService alongside
// StatsService.
type HandlerClient struct {
	Address string
	// Dial connects to the HandlerService; nil dials Address with
	// grpc.NewClient and insecure credentials. Overridden in tests.
	Dial func(ctx context.Context) (command.HandlerServiceClient, func(), error)
}

func (c HandlerClient) connect(ctx context.Context) (command.HandlerServiceClient, func(), error) {
	dial := c.Dial
	if dial == nil {
		dial = func(context.Context) (command.HandlerServiceClient, func(), error) {
			conn, err := grpc.NewClient(c.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return nil, nil, err
			}
			return command.NewHandlerServiceClient(conn), func() { _ = conn.Close() }, nil
		}
	}
	client, closeConn, err := dial(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to xray handler API: %w", err)
	}
	return client, closeConn, nil
}

// AddUser pushes one roster user onto one inbound via AlterInbound's
// AddUserOperation. xray answers a duplicate email with "already exists"
// (docs/research/handlerservice-live-user-management.md §5) — for a
// retrying apply that means the user is there, so it returns nil.
func (c HandlerClient) AddUser(ctx context.Context, tag string, user ManagedUser) error {
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()

	client, closeConn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer closeConn()

	_, err = client.AlterInbound(ctx, &command.AlterInboundRequest{
		Tag: tag,
		Operation: serial.ToTypedMessage(&command.AddUserOperation{
			User: &protocol.User{
				Level: 0,
				Email: user.Email,
				Account: serial.ToTypedMessage(&vless.Account{
					Id:   user.ID,
					Flow: user.Flow,
				}),
			},
		}),
	})
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("add %s to inbound %s: %w", user.Email, tag, err)
	}
	return nil
}
