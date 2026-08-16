package xraystatus

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// BinaryVersion reads the xray binary's version by running it.
type BinaryVersion struct {
	// Run executes the binary and returns its output; nil uses os/exec.
	// Overridden in tests.
	Run func(ctx context.Context, execPath string) ([]byte, error)
}

// Version implements VersionRunner.
func (b BinaryVersion) Version(ctx context.Context, execPath string) (string, error) {
	run := b.Run
	if run == nil {
		run = func(ctx context.Context, path string) ([]byte, error) {
			return exec.CommandContext(ctx, path, "version").Output()
		}
	}
	output, err := run(ctx, execPath)
	if err != nil {
		return "", fmt.Errorf("run %s version: %w", execPath, err)
	}
	version, ok := parseVersion(string(output))
	if !ok {
		return "", fmt.Errorf("parse %s version output: %q", execPath, strings.TrimSpace(string(output)))
	}
	return version, nil
}

// parseVersion extracts the version from `xray version` output, e.g.
// "Xray 26.4.13 (Xray, Penetrates Everything.) 8ab9a61 (go1.26.1 linux/amd64)".
func parseVersion(output string) (string, bool) {
	fields := strings.Fields(output)
	if len(fields) < 2 || fields[0] != "Xray" {
		return "", false
	}
	return fields[1], true
}
