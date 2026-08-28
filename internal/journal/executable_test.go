package journal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateExecutableAcceptsARegularExecutableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journalctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := ValidateExecutable(path); err != nil {
		t.Errorf("ValidateExecutable() error = %v, want nil", err)
	}
}

func TestValidateExecutableFollowsARootConfiguredSymlink(t *testing.T) {
	// Distributions ship /usr/bin/journalctl as a symlink, so the link is
	// followed — but what it lands on must still be a regular executable.
	dir := t.TempDir()
	target := filepath.Join(dir, "journalctl.real")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "journalctl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := ValidateExecutable(link); err != nil {
		t.Errorf("ValidateExecutable() error = %v, want nil", err)
	}
}

func TestValidateExecutableRejectsUnsafePaths(t *testing.T) {
	dir := t.TempDir()

	regular := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(regular, []byte("plain"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	directory := filepath.Join(dir, "directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	danglingLink := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "absent"), danglingLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	directoryLink := filepath.Join(dir, "directory-link")
	if err := os.Symlink(directory, directoryLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "relative", path: "usr/bin/journalctl"},
		{name: "bare command name", path: "journalctl"},
		{name: "missing", path: filepath.Join(dir, "absent")},
		{name: "not executable", path: regular},
		{name: "a directory", path: directory},
		{name: "a symlink to nowhere", path: danglingLink},
		{name: "a symlink to a directory", path: directoryLink},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateExecutable(test.path); err == nil {
				t.Errorf("ValidateExecutable(%q) error = nil, want a rejection", test.path)
			}
		})
	}
}
