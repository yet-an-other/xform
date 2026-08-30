package journal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// ValidateExecutable reports whether a configured journalctl path is safe to
// run: absolute, resolvable, a regular file, and executable by the Panel
// user (SPEC §8).
//
// A root-configured symlink is followed deliberately — distributions ship
// /usr/bin/journalctl as one — but what it resolves to must still be a
// regular executable file, never a directory, device, or socket.
//
// Startup calls this once and refuses to start on failure. The reader calls
// it again before every collection, so a path that later disappears or turns
// unsafe costs the Panel this one feature rather than its monitoring.
func ValidateExecutable(path string) error {
	if path == "" {
		return errors.New("no path is configured")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s is not an absolute path", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("%s cannot be resolved", path)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("%s cannot be read", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if !executableBy(info, os.Geteuid(), os.Getegid()) {
		return fmt.Errorf("%s is not executable by this user", path)
	}
	return nil
}

// executableBy reports whether the effective user may execute the file,
// reading the permission triad that actually applies to it rather than any
// execute bit — a root-only 0700 helper is not executable by the Panel.
func executableBy(info os.FileInfo, euid, egid int) bool {
	const (
		ownerExecute = 0o100
		groupExecute = 0o010
		otherExecute = 0o001
	)
	mode := info.Mode().Perm()

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// A platform without POSIX ownership: any execute bit is all there is
		// to check.
		return mode&(ownerExecute|groupExecute|otherExecute) != 0
	}
	if euid == 0 {
		// root may execute a file carrying any execute bit at all.
		return mode&(ownerExecute|groupExecute|otherExecute) != 0
	}
	if int(stat.Uid) == euid {
		return mode&ownerExecute != 0
	}
	if int(stat.Gid) == egid || inSupplementaryGroup(int(stat.Gid)) {
		return mode&groupExecute != 0
	}
	return mode&otherExecute != 0
}

func inSupplementaryGroup(gid int) bool {
	groups, err := os.Getgroups()
	if err != nil {
		return false
	}
	return slices.Contains(groups, gid)
}
