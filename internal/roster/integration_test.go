package roster_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/roster"
	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// The acceptance path end to end over the real seams (issue #53): the POST's
// store, the real SQLite roster, the real file render — only the gRPC push
// is faked. Adding a user makes the file carry them and xray receive them.
func TestAddEndToEndOverTheRealStoreAndRenderer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	document := `{
  "inbounds": [
    {"tag": "vless-vision", "protocol": "vless",
     "settings": {"clients": [{"email": "existing@example.com", "id": "uuid-existing", "flow": "xtls-rprx-vision"}]},
     "streamSettings": {"network": "tcp", "security": "reality"}}
  ]
}`
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	view, err := xrayconfig.ParseView([]byte(document))
	if err != nil {
		t.Fatalf("parse view: %v", err)
	}

	store, err := users.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events := &[]string{}
	pusher := &fakePusher{events: events}
	service := roster.NewService(
		store,
		&fakeViews{view: view},
		&fakeParseSource{version: 1, parse: xrayconfig.RosterParse{Labels: map[string]xrayconfig.User{}, Clients: map[string]xrayconfig.Client{}}},
		roster.FileRenderer{Path: configPath},
		pusher,
		&fakeStatus{status: "running"},
		make(chan struct{}, 1),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.Start(ctx)

	result, err := service.Add(context.Background(), "alice@example.com", "", []string{"vless-vision"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if result.Sync != roster.Synced {
		t.Fatalf("sync = %q, want synced", result.Sync)
	}

	// The file carries alice — an xray restart keeps her — with the existing
	// client untouched and the vision flow copied.
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	parsed, err := xrayconfig.Parse(rendered)
	if err != nil {
		t.Fatalf("rendered config does not parse: %v\n%s", err, rendered)
	}
	if _, ok := parsed["alice@example.com"]; !ok {
		t.Errorf("the file does not carry alice:\n%s", rendered)
	}
	if _, ok := parsed["existing@example.com"]; !ok {
		t.Errorf("the existing client must survive:\n%s", rendered)
	}

	// The store row drives the dashboard immediately.
	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if len(list) != 1 || list[0].ClientID == nil || *list[0].ClientID != result.User.ClientID {
		t.Fatalf("users = %+v, want alice with her generated Client ID", list)
	}

	// And the live push carried the copied vision flow.
	if len(pusher.pushed) != 1 || pusher.pushed[0].Flow != "xtls-rprx-vision" {
		t.Errorf("pushed = %+v, want one push with the vision flow", pusher.pushed)
	}
}

// The edit acceptance path end to end over the real seams (issue #54): the
// PATCH's store, the real file surgery — detach, rotate, attach in one
// config — and the live pushes; only the gRPC side is faked.
func TestEditEndToEndOverTheRealStoreAndRenderer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	document := `{
  "inbounds": [
    {"tag": "vless-vision", "protocol": "vless",
     "settings": {"clients": [{"email": "existing@example.com", "id": "uuid-existing", "flow": "xtls-rprx-vision"}]}},
    {"tag": "vless-ws", "protocol": "vless",
     "settings": {"clients": [{"email": "existing@example.com", "id": "uuid-existing"}]}}
  ]
}`
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	view, err := xrayconfig.ParseView([]byte(document))
	if err != nil {
		t.Fatalf("parse view: %v", err)
	}

	store, err := users.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events := &[]string{}
	pusher := &fakePusher{events: events}
	service := roster.NewService(
		store,
		&fakeViews{view: view},
		&fakeParseSource{version: 1, parse: xrayconfig.RosterParse{Labels: map[string]xrayconfig.User{}, Clients: map[string]xrayconfig.Client{}}},
		roster.FileRenderer{Path: configPath},
		pusher,
		&fakeStatus{status: "running"},
		make(chan struct{}, 1),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.Start(ctx)

	added, err := service.Add(ctx, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added.Sync != roster.Synced {
		t.Fatalf("add sync = %q, want synced", added.Sync)
	}

	// One save doing all three: rotate the Client ID, keep vision, attach ws.
	edited, err := service.Edit(ctx, "alice@example.com", roster.EditRequest{
		ClientID: "2d37a118-4f1b-4dc0-9e3c-3426b07518df",
		Inbounds: []string{"vless-vision", "vless-ws"},
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Sync != roster.Synced {
		t.Fatalf("edit sync = %q, want synced", edited.Sync)
	}

	// The file: the rotated id in place on vision, the new attachment on ws,
	// everything else byte-stable.
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(rendered), `"id": "2d37a118-4f1b-4dc0-9e3c-3426b07518df"`) {
		t.Errorf("the rotated id must be in the file:\n%s", rendered)
	}
	if strings.Contains(string(rendered), "1d37a118") {
		t.Errorf("the old id must be gone:\n%s", rendered)
	}
	clients, err := xrayconfig.Parse(rendered)
	if err != nil {
		t.Fatalf("rendered config does not parse: %v\n%s", err, rendered)
	}
	for _, email := range []string{"alice@example.com", "existing@example.com"} {
		if _, ok := clients[email]; !ok {
			t.Errorf("the file must keep %s:\n%s", email, rendered)
		}
	}

	// The live pushes: the rotation's remove+add on vision, the attach on ws.
	if !slices.Equal(pusher.removed, []string{"alice@example.com off vless-vision"}) {
		t.Errorf("live removals = %v", pusher.removed)
	}
	rotated := 0
	for _, push := range pusher.pushed {
		if push.ID == "2d37a118-4f1b-4dc0-9e3c-3426b07518df" {
			rotated++
		}
	}
	if rotated != 2 {
		t.Errorf("pushes with the rotated id = %d, want one per attached inbound (pushes: %+v)", rotated, pusher.pushed)
	}

	// The store carries the final record; an idempotent re-save applies
	// nothing.
	records := len(pusher.pushed)
	again, err := service.Edit(ctx, "alice@example.com", roster.EditRequest{
		ClientID: "2d37a118-4f1b-4dc0-9e3c-3426b07518df",
		Inbounds: []string{"vless-vision", "vless-ws"},
	})
	if err != nil {
		t.Fatalf("repeat edit: %v", err)
	}
	if again.Sync != roster.Synced || again.User.UpdatedAt != edited.User.UpdatedAt {
		t.Errorf("repeat edit = %+v, want the same state untouched", again)
	}
	if len(pusher.pushed) != records {
		t.Error("an idempotent re-save must push nothing")
	}

	// And detaching everything leaves the roster member in the store.
	detached, err := service.Edit(ctx, "alice@example.com", roster.EditRequest{Inbounds: []string{}})
	if err != nil {
		t.Fatalf("detach-all: %v", err)
	}
	if detached.Sync != roster.Synced || len(detached.User.Inbounds) != 0 {
		t.Fatalf("detach-all = %+v, want synced and profile-less", detached)
	}
	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	alice := list[0]
	for _, user := range list {
		if user.Email == "alice@example.com" {
			alice = user
		}
	}
	if alice.Disabled || alice.ClientID == nil {
		t.Errorf("alice = %+v, want a listed profile-less roster member", alice)
	}
}

// The remove acceptance path end to end over the real seams (issue #55):
// the DELETE's store, the real file surgery out of every attached inbound,
// the live pushes — and the history row that stays behind the gone badge.
func TestRemoveEndToEndOverTheRealStoreAndRenderer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	document := `{
  "inbounds": [
    {"tag": "vless-vision", "protocol": "vless",
     "settings": {"clients": [
       {"email": "alice@example.com", "id": "uuid-alice"},
       {"email": "existing@example.com", "id": "uuid-existing", "flow": "xtls-rprx-vision"}
     ]}},
    {"tag": "vless-ws", "protocol": "vless",
     "settings": {"clients": [{"email": "alice@example.com", "id": "uuid-alice"}]}}
  ]
}`
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	view, err := xrayconfig.ParseView([]byte(document))
	if err != nil {
		t.Fatalf("parse view: %v", err)
	}

	store, err := users.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	// Seed: alice carries traffic history, and the config parse adopts her
	// into the roster with both attachments.
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
	}, nil, &users.RosterParse{
		Labels: map[string]users.RosterUser{
			"alice@example.com":    {Protocol: "VLESS", Security: "Reality"},
			"existing@example.com": {Protocol: "VLESS", Security: "Reality"},
		},
		Clients: map[string]users.RosterClient{
			"alice@example.com":    {ClientID: "uuid-alice", Inbounds: []string{"vless-vision", "vless-ws"}},
			"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision"}},
		},
	}, time.Unix(1_780_000_000, 0)); err != nil {
		t.Fatalf("seed poll: %v", err)
	}
	events := &[]string{}
	pusher := &fakePusher{events: events}
	service := roster.NewService(
		store,
		&fakeViews{view: view},
		&fakeParseSource{version: 1, parse: xrayconfig.RosterParse{Labels: map[string]xrayconfig.User{}, Clients: map[string]xrayconfig.Client{}}},
		roster.FileRenderer{Path: configPath},
		pusher,
		&fakeStatus{status: "running"},
		make(chan struct{}, 1),
	)
	cancelCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	service.Start(cancelCtx)

	if _, err := store.RosterRecord(ctx, "alice@example.com"); err != nil {
		t.Fatalf("adoption seed: %v", err)
	}

	sync, removed, err := service.Disable(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed || sync != roster.Synced {
		t.Fatalf("remove = %t / %q, want a live synced removal", removed, sync)
	}

	// The file no longer lists her anywhere — an xray restart keeps her
	// gone — and the neighbours stay byte-stable.
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(rendered), "alice@example.com") {
		t.Errorf("the file must no longer list alice:\n%s", rendered)
	}
	clients, err := xrayconfig.Parse(rendered)
	if err != nil {
		t.Fatalf("rendered config does not parse: %v\n%s", err, rendered)
	}
	if _, ok := clients["existing@example.com"]; !ok {
		t.Errorf("the existing client must survive:\n%s", rendered)
	}

	// The live removals covered both inbounds.
	if !slices.Equal(pusher.removedList(), []string{
		"alice@example.com off vless-vision",
		"alice@example.com off vless-ws",
	}) {
		t.Errorf("live removals = %v", pusher.removedList())
	}

	// The history row stays: gone, totals intact, roster fields null.
	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	var alice users.User
	for _, user := range list {
		if user.Email == "alice@example.com" {
			alice = user
		}
	}
	if !alice.Disabled || alice.UpBytesTotal != 100 || alice.DownBytesTotal != 1_000 || alice.ClientID != nil {
		t.Errorf("alice = %+v, want gone with history and no roster fields", alice)
	}

	// And a config parse carrying her again (drift before the render landed
	// cannot happen here, but a stale parse may race) does not revive her.
	if err := store.ApplyPoll(ctx, nil, nil, &users.RosterParse{
		Labels: map[string]xrayconfig.User{"alice@example.com": {Protocol: "VLESS", Security: "Reality"}},
	}, time.Unix(1_780_010_000, 0)); err != nil {
		t.Fatalf("apply drift parse: %v", err)
	}
	list, err = store.Users(ctx)
	if err != nil {
		t.Fatalf("users after parse: %v", err)
	}
	for _, user := range list {
		if user.Email == "alice@example.com" && (!user.Disabled || user.ClientID != nil) {
			t.Errorf("after the parse alice = %+v, want still gone", user)
		}
	}

	// Idempotent: a second remove is a no-op success.
	if sync, removedAgain, err := service.Disable(ctx, "alice@example.com"); err != nil || sync != roster.Synced || removedAgain {
		t.Errorf("re-remove = %q / %v / %t, want synced, removed nothing", sync, err, removedAgain)
	}
}

// The convergence acceptance path end to end over the real seams (issue
// #56): a store user hand-deleted from the config file comes back — file
// and live — within a watch tick, with the store untouched.
func TestConvergeEndToEndOverTheRealStoreAndRenderer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	document := `{
  "inbounds": [
    {"tag": "vless-vision", "protocol": "vless",
     "settings": {"clients": [{"email": "existing@example.com", "id": "uuid-existing", "flow": "xtls-rprx-vision"}]}}
  ]
}`
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	view, err := xrayconfig.ParseView([]byte(document))
	if err != nil {
		t.Fatalf("parse view: %v", err)
	}

	store, err := users.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	events := &[]string{}
	pusher := &fakePusher{events: events}
	parses := &fakeParseSource{version: 1, parse: xrayconfig.RosterParse{
		Labels: map[string]xrayconfig.User{},
		Clients: map[string]xrayconfig.Client{
			"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision"}},
		},
	}}
	changes := make(chan struct{}, 1)
	service := roster.NewService(
		store,
		&fakeViews{view: view},
		parses,
		roster.FileRenderer{Path: configPath},
		pusher,
		&fakeStatus{status: "running"},
		changes,
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.Start(ctx)

	added, err := service.Add(ctx, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added.Sync != roster.Synced {
		t.Fatalf("add sync = %q, want synced", added.Sync)
	}

	// The hand edit: the file loses alice — the panel is not the only
	// writer — and the watcher parses the drifted file.
	drifted := `{
  "inbounds": [
    {"tag": "vless-vision", "protocol": "vless",
     "settings": {"clients": [{"email": "existing@example.com", "id": "uuid-existing", "flow": "xtls-rprx-vision"}]}}
  ]
}`
	if err := os.WriteFile(configPath, []byte(drifted), 0o600); err != nil {
		t.Fatalf("hand edit: %v", err)
	}
	parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision"}},
	})

	// The watch tick: the panel restores alice into the file and pushes her
	// back to xray.
	changes <- struct{}{}
	eventually(t, "the file carries alice again", func() bool {
		rendered, err := os.ReadFile(configPath)
		if err != nil {
			return false
		}
		clients, err := xrayconfig.Parse(rendered)
		return err == nil && func() bool { _, ok := clients["alice@example.com"]; return ok }()
	})

	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(rendered), `"id": "uuid-wrong"`) {
		t.Errorf("unexpected drift in the restored file:\n%s", rendered)
	}
	clients, err := xrayconfig.Parse(rendered)
	if err != nil {
		t.Fatalf("restored config does not parse: %v\n%s", err, rendered)
	}
	if _, ok := clients["existing@example.com"]; !ok {
		t.Errorf("the existing client must survive:\n%s", rendered)
	}
	if got := pusher.lastAdd(); got.Email != "alice@example.com" || got.Flow != "xtls-rprx-vision" {
		t.Errorf("final push = %+v, want alice restored with the vision flow", got)
	}

	// The store was never touched by the drift.
	record, err := store.RosterRecord(ctx, "alice@example.com")
	if err != nil || !slices.Equal(record.Inbounds, []string{"vless-vision"}) {
		t.Errorf("record after converge = %+v / %v, want untouched", record, err)
	}

	// And the next tick over the now-consistent parse is quiet.
	pushes, _ := pusher.counts()
	parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision"}},
		"alice@example.com":    {ClientID: "1d37a118-4f1b-4dc0-9e3c-3426b07518df", Inbounds: []string{"vless-vision"}},
	})
	changes <- struct{}{}
	eventually(t, "the echo tick stayed quiet", func() bool {
		return len(pusher.pushed) == pushes && service.Sync() == roster.Synced
	})
}
