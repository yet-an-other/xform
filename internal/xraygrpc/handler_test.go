package xraygrpc_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	command "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/proxy/vless"

	"github.com/yet-an-other/xform/internal/xraygrpc"
)

// fakeHandlerService implements the generated command.HandlerServiceClient,
// capturing AlterInbound requests.
type fakeHandlerService struct {
	got  []*command.AlterInboundRequest
	err  error
	errs map[string]error // tag → forced error
}

func (f *fakeHandlerService) AlterInbound(_ context.Context, req *command.AlterInboundRequest, _ ...grpc.CallOption) (*command.AlterInboundResponse, error) {
	f.got = append(f.got, req)
	if err, ok := f.errs[req.Tag]; ok {
		return nil, err
	}
	return &command.AlterInboundResponse{}, f.err
}

func (f *fakeHandlerService) AddInbound(context.Context, *command.AddInboundRequest, ...grpc.CallOption) (*command.AddInboundResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeHandlerService) RemoveInbound(context.Context, *command.RemoveInboundRequest, ...grpc.CallOption) (*command.RemoveInboundResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeHandlerService) ListInbounds(context.Context, *command.ListInboundsRequest, ...grpc.CallOption) (*command.ListInboundsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeHandlerService) GetInboundUsers(context.Context, *command.GetInboundUserRequest, ...grpc.CallOption) (*command.GetInboundUserResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeHandlerService) GetInboundUsersCount(context.Context, *command.GetInboundUserRequest, ...grpc.CallOption) (*command.GetInboundUsersCountResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeHandlerService) AddOutbound(context.Context, *command.AddOutboundRequest, ...grpc.CallOption) (*command.AddOutboundResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeHandlerService) RemoveOutbound(context.Context, *command.RemoveOutboundRequest, ...grpc.CallOption) (*command.RemoveOutboundResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeHandlerService) AlterOutbound(context.Context, *command.AlterOutboundRequest, ...grpc.CallOption) (*command.AlterOutboundResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func (f *fakeHandlerService) ListOutbounds(context.Context, *command.ListOutboundsRequest, ...grpc.CallOption) (*command.ListOutboundsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not needed by the panel")
}

func dialToHandler(client command.HandlerServiceClient) func(context.Context) (command.HandlerServiceClient, func(), error) {
	return func(context.Context) (command.HandlerServiceClient, func(), error) {
		return client, func() {}, nil
	}
}

// AddUser is the live apply path (user-management spec §4): an AlterInbound
// AddUserOperation carrying the user's email identity, Client ID, and the
// attach-time flow.
func TestAddUserPushesTheAddOperation(t *testing.T) {
	service := &fakeHandlerService{}
	client := xraygrpc.HandlerClient{Address: "127.0.0.1:8080", Dial: dialToHandler(service)}

	err := client.AddUser(context.Background(), "vless-vision", xraygrpc.ManagedUser{
		Email: "alice@example.com",
		ID:    "1d37a118-4f1b-4dc0-9e3c-3426b07518df",
		Flow:  "xtls-rprx-vision",
	})
	if err != nil {
		t.Fatalf("add user: %v", err)
	}

	if len(service.got) != 1 {
		t.Fatalf("AlterInbound calls = %d, want 1", len(service.got))
	}
	request := service.got[0]
	if request.Tag != "vless-vision" {
		t.Errorf("tag = %q, want vless-vision", request.Tag)
	}
	operation, err := request.Operation.GetInstance()
	if err != nil {
		t.Fatalf("decode operation: %v", err)
	}
	add, ok := operation.(*command.AddUserOperation)
	if !ok {
		t.Fatalf("operation = %T, want AddUserOperation", operation)
	}
	if add.User.Email != "alice@example.com" {
		t.Errorf("email = %q", add.User.Email)
	}
	account, err := add.User.Account.GetInstance()
	if err != nil {
		t.Fatalf("decode account: %v", err)
	}
	vlessAccount, ok := account.(*vless.Account)
	if !ok {
		t.Fatalf("account = %T, want vless.Account", account)
	}
	if vlessAccount.Id != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" || vlessAccount.Flow != "xtls-rprx-vision" {
		t.Errorf("account = %+v, want the Client ID and vision flow", vlessAccount)
	}
}

// xray answers a duplicate email with "already exists" — for the apply path
// that is success: the user is there. Anything else is a push failure.
func TestAddUserTreatsAlreadyExistsAsApplied(t *testing.T) {
	service := &fakeHandlerService{err: status.Error(codes.Unknown, "User alice@example.com already exists.")}
	client := xraygrpc.HandlerClient{Address: "127.0.0.1:8080", Dial: dialToHandler(service)}

	if err := client.AddUser(context.Background(), "vless-vision", xraygrpc.ManagedUser{Email: "alice@example.com", ID: "id"}); err != nil {
		t.Errorf("duplicate email must read as applied, got %v", err)
	}
}

func TestAddUserSurfacesPushFailures(t *testing.T) {
	service := &fakeHandlerService{err: status.Error(codes.Unknown, "failed to get handler: vless-vision")}
	client := xraygrpc.HandlerClient{Address: "127.0.0.1:8080", Dial: dialToHandler(service)}

	err := client.AddUser(context.Background(), "vless-vision", xraygrpc.ManagedUser{Email: "alice@example.com", ID: "id"})
	if err == nil || errors.Is(err, xraygrpc.ErrUserExists) {
		t.Errorf("a missing inbound is a push failure, got %v", err)
	}
}

// RemoveUser detaches one roster user from one inbound (user-management
// spec §4): a RemoveUserOperation keyed by email — the identity xray's
// handler manager indexes users by.
func TestRemoveUserPushesTheRemoveOperation(t *testing.T) {
	service := &fakeHandlerService{}
	client := xraygrpc.HandlerClient{Address: "127.0.0.1:8080", Dial: dialToHandler(service)}

	if err := client.RemoveUser(context.Background(), "vless-vision", "alice@example.com"); err != nil {
		t.Fatalf("remove user: %v", err)
	}

	if len(service.got) != 1 {
		t.Fatalf("AlterInbound calls = %d, want 1", len(service.got))
	}
	request := service.got[0]
	if request.Tag != "vless-vision" {
		t.Errorf("tag = %q, want vless-vision", request.Tag)
	}
	operation, err := request.Operation.GetInstance()
	if err != nil {
		t.Fatalf("decode operation: %v", err)
	}
	remove, ok := operation.(*command.RemoveUserOperation)
	if !ok {
		t.Fatalf("operation = %T, want RemoveUserOperation", operation)
	}
	if remove.Email != "alice@example.com" {
		t.Errorf("email = %q", remove.Email)
	}
}

// xray answers a missing email with "not found" — the remove counterpart of
// the add's "already exists": for a retrying apply that means the user is
// already gone, so it reads as success.
func TestRemoveUserTreatsNotFoundAsApplied(t *testing.T) {
	service := &fakeHandlerService{err: status.Error(codes.Unknown, "User alice@example.com not found.")}
	client := xraygrpc.HandlerClient{Address: "127.0.0.1:8080", Dial: dialToHandler(service)}

	if err := client.RemoveUser(context.Background(), "vless-vision", "alice@example.com"); err != nil {
		t.Errorf("a missing email must read as applied, got %v", err)
	}
}

func TestRemoveUserSurfacesPushFailures(t *testing.T) {
	service := &fakeHandlerService{err: status.Error(codes.Unknown, "failed to get handler: no-such-tag")}
	client := xraygrpc.HandlerClient{Address: "127.0.0.1:8080", Dial: dialToHandler(service)}

	if err := client.RemoveUser(context.Background(), "no-such-tag", "alice@example.com"); err == nil {
		t.Error("a missing inbound is a push failure, not success")
	}
}
