package roster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// FileRenderer is the production Renderer: the file half of the apply path
// (user-management spec §4). The render is raw-span surgery on the document
// as it sits on disk, and the write is atomic — a temp file in the same
// directory, then rename — so a crash never leaves a half-written config.
// The rename requires write access on the config's directory (§8).
type FileRenderer struct {
	Path string
}

// Render reads the config and applies the plan inside the managed clients
// arrays, persisting the result when it changed. An unchanged render never
// writes — no watcher echo, no mtime churn.
func (r FileRenderer) Render(_ context.Context, plan xrayconfig.RenderPlan) (bool, error) {
	document, err := os.ReadFile(r.Path)
	if err != nil {
		return false, fmt.Errorf("read xray config: %w", err)
	}
	rendered, changed, err := xrayconfig.RenderClients(document, plan)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}

	info, err := os.Stat(r.Path)
	if err != nil {
		return false, fmt.Errorf("stat xray config: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(r.Path), ".xform-render-*")
	if err != nil {
		return false, fmt.Errorf("create render temp file: %w", err)
	}
	defer func() { _ = os.Remove(temp.Name()) }() // a no-op once the rename lands
	if _, err := temp.Write(rendered); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("write render temp file: %w", err)
	}
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("preserve config permissions: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("close render temp file: %w", err)
	}
	if err := os.Rename(temp.Name(), r.Path); err != nil {
		return false, fmt.Errorf("replace xray config: %w", err)
	}
	return true, nil
}
