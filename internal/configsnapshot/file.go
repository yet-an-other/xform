package configsnapshot

import (
	"io"
	"os"
	"syscall"
)

// target is what the configured path landed on: its content stream, plus the
// one property the reader judges it by (§6.5 requires the opened target to be
// a regular file).
type target struct {
	content io.ReadCloser
	// regular is false for anything else — a directory, device, socket, or
	// FIFO. None of those is the configured file, and a FIFO is not even
	// bounded by a size.
	regular bool
}

// openFile is the filesystem seam (§4.3) between the production os adapter
// and the fakes the byte-bound and failure tests run against. It stays
// unexported so no caller can substitute its own filesystem for the real one.
type openFile func(path string) (target, error)

// openPath is the production adapter. It follows a root-configured symlink —
// a deployment may point XFORM_XRAY_CONFIG at one — and then judges the file
// it actually landed on.
//
// The open is non-blocking because opening a FIFO for reading otherwise parks
// until a writer appears: a named pipe at the configured path would hang the
// request rather than be rejected as the non-regular file it is. On a regular
// file the flag changes nothing about the read that follows.
func openPath(path string) (target, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return target{}, err
	}
	// The stat rides on the open handle rather than the path, so the file
	// judged is the file read even if the path is replaced in between.
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return target{}, err
	}
	return target{content: file, regular: info.Mode().IsRegular()}, nil
}
