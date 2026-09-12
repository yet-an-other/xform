// Package listener creates the Panel's HTTP listener. TCP addresses retain
// the standard net package behavior; unix:/absolute/path addresses get the
// pathname ownership and cleanup rules required by a same-Host gateway.
package listener

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	unixPrefix       = "unix:"
	unixSocketMode   = 0o660
	unixProbeTimeout = 100 * time.Millisecond
	renameAttempts   = 8
)

// Listen returns a ready listener for address. Addresses beginning with
// unix: use a pathname Unix socket; every other address is passed to
// net.Listen as a TCP address for compatibility with the existing Panel.
// The parent directory of a Unix socket must already exist: this module does
// not create or change the ownership of gateway directories.
func Listen(address string) (net.Listener, error) {
	if !strings.HasPrefix(address, unixPrefix) {
		return net.Listen("tcp", address)
	}

	path := strings.TrimPrefix(address, unixPrefix)
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("Unix socket path must be absolute")
	}
	if strings.HasSuffix(path, string(filepath.Separator)) {
		return nil, errors.New("Unix socket path must not end with a slash")
	}
	return listenUnix(path)
}

func listenUnix(path string) (net.Listener, error) {
	socket, err := newSocketPath(path)
	if err != nil {
		return nil, err
	}
	if err := prepareSocketPath(socket); err != nil {
		_ = socket.close()
		return nil, err
	}

	unixListener, err := socket.bind()
	if err != nil {
		_ = socket.close()
		return nil, fmt.Errorf("bind Unix socket: %w", err)
	}
	if socket.identity.uid != uint32(os.Geteuid()) {
		_ = unixListener.Close()
		_ = socket.cleanup()
		_ = socket.close()
		return nil, errors.New("new Unix socket is not owned by the Panel user")
	}

	return &ownedUnixListener{
		UnixListener: unixListener,
		socket:       socket,
	}, nil
}

func newSocketPath(path string) (socketPath, error) {
	parentFD, err := openParentDirectory(path)
	if err != nil {
		return socketPath{}, err
	}
	return socketPath{
		path:     path,
		name:     filepath.Base(path),
		parentFD: parentFD,
	}, nil
}

// openParentDirectory opens every parent component without following
// symlinks and keeps the final directory descriptor open. All subsequent
// pathname operations are relative to this descriptor, so a renamed ancestor
// cannot redirect the listener outside the directory that was checked.
//
// The final directory must not be group- or world-writable. The gateway gets
// write access to the socket itself, while only the Panel user can replace
// directory entries or race detached cleanup.
func openParentDirectory(path string) (int, error) {
	parent := filepath.Dir(path)
	flags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	directory, err := unix.Open(string(filepath.Separator), flags, 0)
	if err != nil {
		return -1, fmt.Errorf("open Unix socket root: %w", err)
	}

	components := strings.Split(strings.TrimPrefix(parent, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		next, err := unix.Openat(directory, component, flags, 0)
		if err != nil {
			_ = unix.Close(directory)
			return -1, fmt.Errorf("inspect Unix socket directory: %w", err)
		}
		if err := unix.Close(directory); err != nil {
			_ = unix.Close(next)
			return -1, fmt.Errorf("close Unix socket directory: %w", err)
		}
		directory = next
	}

	var stat unix.Stat_t
	if err := unix.Fstat(directory, &stat); err != nil {
		_ = unix.Close(directory)
		return -1, fmt.Errorf("inspect Unix socket directory: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(directory)
		return -1, errors.New("Unix socket parent is not a directory")
	}
	if uint32(stat.Uid) != uint32(os.Geteuid()) {
		_ = unix.Close(directory)
		return -1, errors.New("Unix socket parent must be owned by the Panel user")
	}
	if stat.Mode&0o022 != 0 {
		_ = unix.Close(directory)
		return -1, errors.New("Unix socket parent must not be group- or world-writable")
	}
	return directory, nil
}

// bind creates and listens on a private temporary pathname, sets its mode,
// and atomically publishes the exact pathname inode as the requested socket
// name. linkat with AT_EMPTY_PATH publishes the O_PATH descriptor itself, so
// a replacement of the temporary pathname cannot be published accidentally.
func (socket *socketPath) bind() (*net.UnixListener, error) {
	for attempt := 0; attempt < renameAttempts; attempt++ {
		temporary, err := temporaryName()
		if err != nil {
			return nil, err
		}
		fileDescriptor, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		bindErr := unix.Bind(fileDescriptor, &unix.SockaddrUnix{Name: socket.procPath(temporary)})
		if errors.Is(bindErr, unix.EADDRINUSE) {
			_ = unix.Close(fileDescriptor)
			continue
		}
		if bindErr != nil {
			_ = unix.Close(fileDescriptor)
			return nil, bindErr
		}

		// fstat on an AF_UNIX socket descriptor reports the sockfs inode,
		// not the pathname inode. The O_PATH descriptor pins the actual
		// pathname object and can be used by linkat without a second lookup.
		pathDescriptor, identity, err := socket.openIdentity(temporary)
		if err != nil {
			_ = unix.Close(fileDescriptor)
			return nil, err
		}
		if err := unix.Listen(fileDescriptor, unix.SOMAXCONN); err != nil {
			_ = unix.Close(pathDescriptor)
			_ = unix.Close(fileDescriptor)
			_ = socket.unlinkDetached(temporary, identity)
			return nil, err
		}
		if err := chmodSocket(pathDescriptor, identity); err != nil {
			_ = unix.Close(pathDescriptor)
			_ = unix.Close(fileDescriptor)
			_ = socket.unlinkDetached(temporary, identity)
			return nil, err
		}
		linkErr := unix.Linkat(pathDescriptor, "", socket.parentFD, socket.name, unix.AT_EMPTY_PATH)
		if isEmptyPathUnsupported(linkErr) {
			// AT_EMPTY_PATH needs CAP_DAC_READ_SEARCH on older kernels and
			// may be denied by a systemd syscall filter. The parent is
			// deliberately non-group/world-writable, so the pathname form
			// retains the containment and ownership invariant.
			linkErr = unix.Linkat(socket.parentFD, temporary, socket.parentFD, socket.name, 0)
		}
		if linkErr != nil {
			_ = unix.Close(pathDescriptor)
			_ = unix.Close(fileDescriptor)
			_ = socket.unlinkDetached(temporary, identity)
			return nil, linkErr
		}
		socket.identity = identity
		if err := socket.unlinkDetached(temporary, identity); err != nil {
			_ = unix.Close(pathDescriptor)
			_ = unix.Close(fileDescriptor)
			_ = socket.cleanup()
			return nil, err
		}
		if err := unix.Close(pathDescriptor); err != nil {
			_ = unix.Close(fileDescriptor)
			_ = socket.cleanup()
			return nil, err
		}

		file := os.NewFile(uintptr(fileDescriptor), socket.path)
		listener, err := net.FileListener(file)
		fileCloseErr := file.Close()
		if err != nil {
			_ = socket.cleanup()
			return nil, err
		}
		if fileCloseErr != nil {
			_ = listener.Close()
			_ = socket.cleanup()
			return nil, fileCloseErr
		}
		unixListener, ok := listener.(*net.UnixListener)
		if !ok {
			_ = listener.Close()
			_ = socket.cleanup()
			return nil, errors.New("Unix socket did not produce a Unix listener")
		}
		// FileListener already disables pathname unlinking, but keep the
		// behavior explicit: cleanup below must compare the pathname identity.
		unixListener.SetUnlinkOnClose(false)
		return unixListener, nil
	}
	return nil, errors.New("could not reserve a temporary Unix socket pathname")
}

// prepareSocketPath checks an existing pathname without following it. Only a
// stale socket owned by this process user may be removed before binding.
func prepareSocketPath(socket socketPath) error {
	info, err := socket.lstat(socket.name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Unix socket path: %w", err)
	}

	if isSymlink(&info) {
		return errors.New("Unix socket path is a symlink")
	}
	if !isSocket(&info) {
		return errors.New("Unix socket path is not a socket")
	}

	existing := identityFromUnixStat(&info)
	if existing.uid != uint32(os.Geteuid()) {
		return errors.New("Unix socket is owned by another user")
	}

	alive, err := socket.isAlive(socket.name)
	if err != nil {
		return fmt.Errorf("probe existing Unix socket: %w", err)
	}
	if alive {
		return errors.New("Unix socket is already in use")
	}

	socket.identity = existing
	return socket.removeStale()
}

func (socket socketPath) openIdentity(name string) (int, fileIdentity, error) {
	fileDescriptor, err := unix.Openat(socket.parentFD, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fileIdentity{}, fmt.Errorf("identify Unix socket: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fileDescriptor, &stat); err != nil {
		_ = unix.Close(fileDescriptor)
		return -1, fileIdentity{}, fmt.Errorf("identify Unix socket: %w", err)
	}
	if !isSocket(&stat) {
		_ = unix.Close(fileDescriptor)
		return -1, fileIdentity{}, errors.New("new Unix listener has no socket pathname")
	}
	identity := identityFromUnixStat(&stat)
	if identity.uid != uint32(os.Geteuid()) {
		_ = unix.Close(fileDescriptor)
		return -1, fileIdentity{}, errors.New("new Unix socket is not owned by the Panel user")
	}
	return fileDescriptor, identity, nil
}

func (socket socketPath) lstat(name string) (unix.Stat_t, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(socket.parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			err = os.ErrNotExist
		}
		return stat, &os.PathError{Op: "lstat", Path: socket.displayPath(name), Err: err}
	}
	return stat, nil
}

func (socket socketPath) displayPath(name string) string {
	if name == socket.name {
		return socket.path
	}
	return filepath.Join(filepath.Dir(socket.path), name)
}

func (socket socketPath) procPath(name string) string {
	return fmt.Sprintf("/proc/self/fd/%d/%s", socket.parentFD, name)
}

func socketIsAlive(path string) (bool, error) {
	connection, err := net.DialTimeout("unix", path, unixProbeTimeout)
	if err == nil {
		_ = connection.Close()
		return true, nil
	}
	if isStaleSocketError(err) {
		return false, nil
	}
	return false, err
}

func (socket socketPath) isAlive(name string) (bool, error) {
	return socketIsAlive(socket.procPath(name))
}

func isStaleSocketError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

type fileIdentity struct {
	dev uint64
	ino uint64
	uid uint32
}

func identityFromFileInfo(info os.FileInfo) (fileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, fmt.Errorf("unexpected file metadata type %T", info.Sys())
	}
	return fileIdentity{
		dev: uint64(stat.Dev),
		ino: uint64(stat.Ino),
		uid: uint32(stat.Uid),
	}, nil
}

func identityFromUnixStat(stat *unix.Stat_t) fileIdentity {
	return fileIdentity{
		dev: uint64(stat.Dev),
		ino: uint64(stat.Ino),
		uid: uint32(stat.Uid),
	}
}

func isSocket(stat *unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFSOCK
}

func isSymlink(stat *unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFLNK
}

type socketPath struct {
	path     string
	name     string
	parentFD int
	identity fileIdentity
}

// detach atomically moves the current pathname to an unguessable temporary
// name. It returns matched=false when another object won the pathname race;
// that object is restored without being removed.
func (socket socketPath) detach() (temporary string, matched, present bool, err error) {
	temporary, err = socket.movePathAside()
	if errors.Is(err, os.ErrNotExist) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}

	info, err := socket.lstat(temporary)
	if err != nil {
		return "", false, true, errors.Join(err, socket.restorePath(temporary))
	}
	if !isSocket(&info) {
		return "", false, true, socket.restoreChangedPath(temporary)
	}
	identity := identityFromUnixStat(&info)
	if identity != socket.identity {
		return "", false, true, socket.restoreChangedPath(temporary)
	}
	return temporary, true, true, nil
}

func (socket socketPath) restoreChangedPath(temporary string) error {
	if err := socket.restorePath(temporary); err != nil {
		return fmt.Errorf("restore changed Unix socket path: %w", err)
	}
	return nil
}

func (socket socketPath) removeStale() error {
	temporary, matched, present, err := socket.detach()
	if err != nil {
		return fmt.Errorf("inspect Unix socket replacement: %w", err)
	}
	if !present {
		return nil
	}
	if !matched {
		return errors.New("Unix socket changed while it was being checked")
	}

	// A listener could have appeared after the first probe but before the
	// atomic detach. Probe the detached inode too, while its identity is still
	// known and before it can be removed.
	alive, err := socket.isAlive(temporary)
	if err != nil {
		_ = socket.restorePath(temporary)
		return fmt.Errorf("probe detached Unix socket: %w", err)
	}
	if alive {
		_ = socket.restorePath(temporary)
		return errors.New("Unix socket is already in use")
	}
	return socket.unlinkDetached(temporary, socket.identity)
}

func (socket socketPath) movePathAside() (string, error) {
	for attempt := 0; attempt < renameAttempts; attempt++ {
		temporary, err := temporaryName()
		if err != nil {
			return "", err
		}
		err = unix.Renameat2(socket.parentFD, socket.name, socket.parentFD, temporary, unix.RENAME_NOREPLACE)
		if err == nil {
			return temporary, nil
		}
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if errors.Is(err, unix.ENOENT) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	return "", errors.New("could not reserve a temporary Unix socket pathname")
}

func temporaryName() (string, error) {
	suffix := make([]byte, 16)
	if _, err := cryptorand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate temporary Unix socket pathname: %w", err)
	}
	return ".xform-" + hex.EncodeToString(suffix), nil
}

func (socket socketPath) restorePath(temporary string) error {
	return unix.Renameat2(socket.parentFD, temporary, socket.parentFD, socket.name, unix.RENAME_NOREPLACE)
}

func chmodSocket(fileDescriptor int, expected fileIdentity) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fileDescriptor, &stat); err != nil {
		return err
	}
	if !isSocket(&stat) || identityFromUnixStat(&stat) != expected {
		return errors.New("Unix socket path changed while setting permissions")
	}

	chmodErr := unix.Fchmodat(fileDescriptor, "", unixSocketMode, unix.AT_EMPTY_PATH)
	if isEmptyPathUnsupported(chmodErr) {
		// Linux kernels before fchmodat2 lack AT_EMPTY_PATH. /proc/self/fd
		// resolves to the already-open O_PATH inode, so this fallback also
		// cannot follow a replacement pathname. It also avoids a possible
		// seccomp denial for the newer fchmodat2 syscall.
		procPath := fmt.Sprintf("/proc/self/fd/%d", fileDescriptor)
		chmodErr = unix.Fchmodat(unix.AT_FDCWD, procPath, unixSocketMode, 0)
	}
	return chmodErr
}

func isEmptyPathUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) ||
		errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.EPERM)
}

func (socket socketPath) unlinkDetached(detachedName string, expected fileIdentity) error {
	info, err := socket.lstat(detachedName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect detached Unix socket: %w", err)
	}
	if !isSocket(&info) {
		return nil
	}
	actual := identityFromUnixStat(&info)
	if actual != expected {
		return nil
	}
	if err := unix.Unlinkat(socket.parentFD, detachedName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove Unix socket: %w", err)
	}
	return nil
}

func (socket socketPath) cleanup() error {
	temporary, matched, present, err := socket.detach()
	if err != nil {
		return err
	}
	if !present || !matched {
		return nil
	}
	return socket.unlinkDetached(temporary, socket.identity)
}

func (socket *socketPath) close() error {
	if socket.parentFD < 0 {
		return nil
	}
	err := unix.Close(socket.parentFD)
	socket.parentFD = -1
	return err
}

type ownedUnixListener struct {
	*net.UnixListener
	socket socketPath

	once     sync.Once
	closeErr error
}

func (listener *ownedUnixListener) Addr() net.Addr {
	return &net.UnixAddr{Name: listener.socket.path, Net: "unix"}
}

func (listener *ownedUnixListener) Close() error {
	listener.once.Do(func() {
		closeErr := listener.UnixListener.Close()
		cleanupErr := listener.socket.cleanup()
		parentErr := listener.socket.close()
		listener.closeErr = errors.Join(closeErr, cleanupErr, parentErr)
	})
	return listener.closeErr
}
