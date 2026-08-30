package journal

import (
	"context"
	"io"
	"os/exec"
)

// childCommand is the exact invocation: an absolute path, separate
// arguments, and a fixed environment. Nothing here goes through a shell.
type childCommand struct {
	Path string
	Args []string
	Env  []string
}

// child is one running journalctl process — the internal seam (SPEC §8) between
// the production os/exec adapter and the fakes the collection tests run
// against. It stays unexported so no caller can substitute its own process
// execution for the fixed one.
type child interface {
	Stdout() io.Reader
	// Stderr is closed when the process ends, which is what lets the reader
	// drain it without a deadline of its own.
	Stderr() io.Reader
	// Wait reaps the process and reports its exit status.
	Wait() error
	// Kill ends the process early. It may be called more than once, from
	// another goroutine, and after Wait has already returned: os.Process
	// reports ErrProcessDone rather than signalling a recycled PID, and any
	// implementation here must be equally safe.
	Kill() error
}

// stderrRead is the capped stderr text and whether more was waiting.
type stderrRead struct {
	text      string
	oversized bool
}

// runningChild pairs a started process with its draining stderr.
type runningChild struct {
	child
	stderr chan stderrRead
}

func (c *runningChild) stderrResult() <-chan stderrRead { return c.stderr }

// startProcess runs journalctl through os/exec: separate arguments, no
// shell, and no inherited environment.
func startProcess(ctx context.Context, command childCommand) (child, error) {
	process := exec.CommandContext(ctx, command.Path, command.Args...)
	process.Env = command.Env
	process.Stdin = nil

	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := process.Start(); err != nil {
		return nil, err
	}
	return &execChild{process: process, stdout: stdout, stderr: stderr}, nil
}

type execChild struct {
	process *exec.Cmd
	stdout  io.ReadCloser
	stderr  io.ReadCloser
}

func (c *execChild) Stdout() io.Reader { return c.stdout }
func (c *execChild) Stderr() io.Reader { return c.stderr }
func (c *execChild) Wait() error       { return c.process.Wait() }

func (c *execChild) Kill() error {
	if c.process.Process == nil {
		return nil
	}
	return c.process.Process.Kill()
}

// countingReader counts what passed through, so a byte cap can be detected
// without holding the bytes.
type countingReader struct {
	reader io.Reader
	count  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	read, err := c.reader.Read(p)
	c.count += int64(read)
	return read, err
}

// readCapped reads at most limit bytes and reports whether more were waiting.
func readCapped(reader io.Reader, limit int64) (string, bool) {
	if reader == nil {
		return "", false
	}
	text, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return string(text), int64(len(text)) > limit
	}
	if int64(len(text)) > limit {
		return string(text[:limit]), true
	}
	return string(text), false
}
