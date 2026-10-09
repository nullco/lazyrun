//go:build linux

package supervisor

import (
	"io"
	"os"
	"sync"

	"github.com/nullco/lazyrun/internal/securefs"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const diagnosticLimit = 1024 * 1024

type boundedDiagnostic struct {
	mu   sync.Mutex
	file *os.File
	size int64
}

func (b *boundedDiagnostic) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n > diagnosticLimit {
		p = p[n-diagnosticLimit:]
	}
	if b.size+int64(len(p)) > diagnosticLimit {
		if err := b.file.Truncate(0); err != nil {
			return 0, err
		}
		if _, err := b.file.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		b.size = 0
	}
	wrote, err := b.file.Write(p)
	b.size += int64(wrote)
	if err != nil {
		return wrote, err
	}
	return n, nil
}

// Supervisor-owned pipe draining, not a dashboard-owned goroutine, bounds both
// diagnostic logging and ordinary stdout/stderr. Startup descriptors are null.
func setupDiagnostics(dir *securefs.Dir) (*logrus.Logger, error) {
	f, err := dir.File("diagnostic.log", unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		return nil, err
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		f.Close()
		return nil, err
	}
	bounded := &boundedDiagnostic{file: f, size: size}
	r, w, err := os.Pipe()
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := unix.Dup2(int(w.Fd()), 1); err != nil {
		r.Close()
		w.Close()
		f.Close()
		return nil, err
	}
	if err := unix.Dup2(int(w.Fd()), 2); err != nil {
		r.Close()
		w.Close()
		f.Close()
		return nil, err
	}
	w.Close()
	go func() {
		defer r.Close()
		defer f.Close()
		buffer := make([]byte, 16*1024)
		for {
			n, err := r.Read(buffer)
			if n > 0 {
				_, _ = bounded.Write(buffer[:n])
			} // Keep draining if the disk fails.
			if err != nil {
				return
			}
		}
	}()
	logger := logrus.New()
	logger.SetOutput(os.Stderr)
	logger.SetFormatter(&logrus.JSONFormatter{})
	return logger, nil
}
