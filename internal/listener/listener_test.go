package listener

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestListenServesHTTPOverTCP(t *testing.T) {
	listener, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if listener.Addr().Network() != "tcp" {
		t.Fatalf("listener network = %q, want tcp", listener.Addr().Network())
	}

	serveHTTPAndClose(t, listener, "http://"+listener.Addr().String(), func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	})
}

func TestListenTrustedRejectsNonLoopbackTCPAddresses(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.10:0", "localhost:0", "127.0.0.1"} {
		t.Run(address, func(t *testing.T) {
			if listener, err := ListenTrusted(address); err == nil {
				_ = listener.Close()
				t.Fatalf("ListenTrusted(%q) succeeded, want an error", address)
			}
		})
	}
}

func TestListenTrustedServesLoopbackTCP(t *testing.T) {
	listener, err := ListenTrusted("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenTrusted() error = %v", err)
	}
	if listener.Addr().Network() != "tcp" {
		t.Fatalf("listener network = %q, want tcp", listener.Addr().Network())
	}

	serveHTTPAndClose(t, listener, "http://"+listener.Addr().String(), func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	})
}

func TestListenTrustedUsesPrivateUnixSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "xform.sock")
	listener, err := ListenTrusted("unix:" + socketPath)
	if err != nil {
		t.Fatalf("ListenTrusted() error = %v", err)
	}
	if listener.Addr().Network() != "unix" {
		t.Fatalf("listener network = %q, want unix", listener.Addr().Network())
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
}

func TestListenServesHTTPOverUnixSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "xform.sock")
	listener, err := Listen("unix:" + socketPath)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if listener.Addr().Network() != "unix" {
		t.Fatalf("listener network = %q, want unix", listener.Addr().Network())
	}

	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("Lstat(socket) error = %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode = %v, want a socket", info.Mode())
	}
	if permissions := info.Mode().Perm(); permissions != 0o660 {
		t.Fatalf("socket permissions = %#o, want 0660", permissions)
	}

	serveHTTPAndClose(t, listener, "http://xform.test/healthz", func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	})
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket after Close() error = %v, want it removed", err)
	}
}

func serveHTTPAndClose(t *testing.T, listener net.Listener, url string, dial func(context.Context) (net.Conn, error)) {
	t.Helper()
	server := &http.Server{
		Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			_, _ = response.Write([]byte("ok"))
		}),
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
	})

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dial(ctx)
	}}
	client := &http.Client{Transport: transport}
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("HTTP request error = %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("HTTP response = %d %q, want 200 %q", response.StatusCode, body, "ok")
	}

	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP server did not stop after listener.Close()")
	}
}

func TestUnixListenRequiresAbsolutePathAndExistingParent(t *testing.T) {
	for _, address := range []string{"unix:", "unix:relative.sock"} {
		t.Run(address, func(t *testing.T) {
			if _, err := Listen(address); err == nil {
				t.Fatalf("Listen(%q) succeeded, want an error", address)
			}
		})
	}

	parent := filepath.Join(t.TempDir(), "missing")
	_, err := Listen("unix:" + filepath.Join(parent, "xform.sock"))
	if err == nil {
		t.Fatal("Listen() succeeded with a missing parent directory")
	}
	if _, statErr := os.Lstat(parent); !os.IsNotExist(statErr) {
		t.Fatalf("missing parent after Listen() error = %v, want it absent", statErr)
	}
}

func TestUnixListenRejectsWritableParent(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o770); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}

	if _, err := Listen("unix:" + filepath.Join(directory, "xform.sock")); err == nil {
		t.Fatal("Listen() accepted a group-writable parent")
	}
}

func TestUnixListenRejectsForeignOwnedParent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing parent ownership requires root")
	}
	directory := t.TempDir()
	foreignUID := 65534
	if foreignUID == os.Geteuid() {
		foreignUID = 65533
	}
	if err := os.Chown(directory, foreignUID, -1); err != nil {
		t.Fatalf("chown parent: %v", err)
	}

	if _, err := Listen("unix:" + filepath.Join(directory, "xform.sock")); err == nil {
		t.Fatal("Listen() accepted a foreign-owned parent")
	}
}

func TestUnixListenRejectsSymlinkedParent(t *testing.T) {
	realDirectory := t.TempDir()
	parent := filepath.Join(t.TempDir(), "socket-dir")
	if err := os.Symlink(realDirectory, parent); err != nil {
		t.Fatalf("symlink parent: %v", err)
	}

	if _, err := Listen("unix:" + filepath.Join(parent, "xform.sock")); err == nil {
		t.Fatal("Listen() followed a symlinked parent, want an error")
	}
	if _, err := os.Lstat(filepath.Join(realDirectory, "xform.sock")); !os.IsNotExist(err) {
		t.Fatalf("socket in symlink target after refused Listen() error = %v", err)
	}
}

func TestUnixListenUsesRetainedParent(t *testing.T) {
	root := t.TempDir()
	checkedParent := filepath.Join(root, "checked")
	replacementParent := filepath.Join(root, "replacement")
	movedParent := filepath.Join(root, "moved")
	for _, directory := range []string{checkedParent, replacementParent} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", directory, err)
		}
	}

	socket, err := newSocketPath(filepath.Join(checkedParent, "xform.sock"))
	if err != nil {
		t.Fatalf("newSocketPath() error = %v", err)
	}
	defer socket.close()
	if err := os.Rename(checkedParent, movedParent); err != nil {
		t.Fatalf("rename checked parent: %v", err)
	}
	if err := os.Symlink(replacementParent, checkedParent); err != nil {
		t.Fatalf("replace checked parent: %v", err)
	}

	listener, err := socket.bind()
	if err != nil {
		t.Fatalf("bind() error = %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	defer os.Remove(filepath.Join(movedParent, "xform.sock"))
	if _, err := os.Lstat(filepath.Join(movedParent, "xform.sock")); err != nil {
		t.Fatalf("socket in retained parent: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(replacementParent, "xform.sock")); !os.IsNotExist(err) {
		t.Fatalf("socket escaped to replacement parent: %v", err)
	}
}

func TestUnixListenRejectsExistingObjects(t *testing.T) {
	tests := []struct {
		name   string
		create func(*testing.T, string)
	}{
		{
			name: "regular file",
			create: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
					t.Fatalf("write file: %v", err)
				}
			},
		},
		{
			name: "directory",
			create: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			},
		},
		{
			name: "symlink",
			create: func(t *testing.T, path string) {
				target := filepath.Join(filepath.Dir(path), "target.sock")
				if err := os.Symlink(target, path); err != nil {
					t.Fatalf("symlink: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "xform.sock")
			test.create(t, path)
			if _, err := Listen("unix:" + path); err == nil {
				t.Fatal("Listen() succeeded, want an error")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("existing object after Listen() error: %v", err)
			}
		})
	}
}

func TestUnixListenRejectsSymlinkToStaleSocket(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: target, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}
	path := filepath.Join(directory, "xform.sock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := Listen("unix:" + path); err == nil {
		t.Fatal("Listen() followed a symlink, want an error")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink after Listen() = %v, want unchanged symlink", err)
	}
}

func TestUnixListenReplacesOwnStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xform.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	before, err := unixPathIdentity(path)
	if err != nil {
		t.Fatalf("identity of stale socket: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}

	listener, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	after, err := unixPathIdentity(path)
	if err != nil {
		t.Fatalf("identity of replacement socket: %v", err)
	}
	if before == after {
		t.Fatal("replacement socket reused the stale socket identity")
	}
}

func TestUnixListenRefusesLiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xform.sock")
	live, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create live socket: %v", err)
	}
	live.SetUnlinkOnClose(false)
	defer func() {
		_ = live.Close()
		_ = os.Remove(path)
	}()

	if _, err := Listen("unix:" + path); err == nil {
		t.Fatal("Listen() succeeded beside a live listener")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("live socket after refused Listen(): %v", err)
	}
}

func TestUnixListenerCloseDoesNotRemoveReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xform.sock")
	listener, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove original pathname: %v", err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create replacement socket: %v", err)
	}
	replacement.SetUnlinkOnClose(false)
	defer func() {
		_ = replacement.Close()
		_ = os.Remove(path)
	}()

	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("replacement after listener.Close(): %v", err)
	}
}

func TestUnixListenRejectsForeignOwnedSocket(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing socket ownership requires root")
	}
	path := filepath.Join(t.TempDir(), "xform.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}
	foreignUID := 65534
	if foreignUID == os.Geteuid() {
		foreignUID = 65533
	}
	if err := os.Chown(path, foreignUID, -1); err != nil {
		t.Fatalf("chown socket: %v", err)
	}

	if _, err := Listen("unix:" + path); err == nil {
		t.Fatal("Listen() replaced a foreign-owned socket")
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("foreign socket after refused Listen(): %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("socket stat type = %T, want *syscall.Stat_t", info.Sys())
	}
	if int(stat.Uid) != foreignUID {
		t.Fatalf("socket uid = %d, want %d", stat.Uid, foreignUID)
	}
}

func unixPathIdentity(path string) (fileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, err
	}
	return identityFromFileInfo(info)
}
