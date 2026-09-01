package roster_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
		fakeViews{view: view},
		roster.FileRenderer{Path: configPath},
		pusher,
		&fakeStatus{status: "running"},
		make(chan struct{}, 1),
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go service.Start(ctx)

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
