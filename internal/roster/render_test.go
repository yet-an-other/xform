package roster_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yet-an-other/xform/internal/roster"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// The file half of the apply path: the render lands atomically, permissions
// survive, and an unchanged re-render never writes (user-management spec §4).
func TestFileRendererWritesAtomicallyAndIdempotently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	document := []byte(`{
  "inbounds": [{"tag": "vless-vision", "protocol": "vless", "settings": {"clients": []}}]
}
`)
	if err := os.WriteFile(path, document, 0o640); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	renderer := roster.FileRenderer{Path: path}
	plan := xrayconfig.RenderPlan{
		Adds: map[string][]xrayconfig.ClientOp{
			"vless-vision": {{Email: "alice@example.com", ID: "uuid-alice", Flow: "xtls-rprx-vision"}},
		},
	}

	changed, err := renderer.Render(context.Background(), plan)
	if err != nil || !changed {
		t.Fatalf("first render: changed = %v, err = %v", changed, err)
	}
	rendered, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rendered: %v", err)
	}
	if _, err := xrayconfig.Parse(rendered); err != nil {
		t.Fatalf("the rendered config must stay parseable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat rendered: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o, want 640 — the temp file takes the config's permissions", info.Mode().Perm())
	}

	// Same plan again: nothing to do, nothing written.
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	changed, err = renderer.Render(context.Background(), plan)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if changed {
		t.Error("second render: changed = true, want a byte-identical no-op")
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an unchanged render must not touch the file")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".xform-render-*")); len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}
