//go:build linux

// Package securefs provides owner-validated, no-symlink directory capabilities.
// Operations use dirfds, not paths that can be redirected after validation.
package securefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type Dir struct {
	file *os.File
	Path string
}

// Open walks absolute paths with O_NOFOLLOW. Root-owned sticky shared ancestors
// (e.g. /tmp) are permitted; the final directory must belong to this user and
// have mode 0700. Missing components are created privately when requested.
func Open(path string, create bool) (*Dir, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("private directory must be absolute: %s", path)
	}
	path = filepath.Clean(path)
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if err := unix.Mkdirat(fd, part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(fd)
				return nil, err
			}
			next, err = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		unix.Close(fd)
		if err != nil {
			return nil, fmt.Errorf("open private directory %s: %w", path, err)
		}
		fd = next
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			unix.Close(fd)
			return nil, err
		}
		trusted := stat.Uid == uint32(os.Geteuid()) || stat.Uid == 0
		shared := stat.Mode&0022 != 0
		stickyRoot := stat.Uid == 0 && stat.Mode&unix.S_ISVTX != 0
		if !trusted || (shared && !stickyRoot) {
			unix.Close(fd)
			return nil, fmt.Errorf("unsafe ancestor of %s: ownership or writable permissions", path)
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0777 != 0700 {
		unix.Close(fd)
		return nil, fmt.Errorf("%s must be owned by uid %d with mode 0700", path, os.Geteuid())
	}
	return &Dir{file: os.NewFile(uintptr(fd), path), Path: path}, nil
}

func (d *Dir) Close() error { return d.file.Close() }
func (d *Dir) FD() int      { return int(d.file.Fd()) }

// ProcPath makes Linux socket paths short, and resolves through an already
// validated directory capability even if an ancestor pathname is renamed.
func (d *Dir) ProcPath(name string) string { return fmt.Sprintf("/proc/self/fd/%d/%s", d.FD(), name) }
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

func (d *Dir) Stat(name string, kind uint32) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if !validName(name) {
		return stat, errors.New("invalid private filename")
	}
	if err := unix.Fstatat(d.FD(), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return stat, err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0777 != 0600 || stat.Mode&unix.S_IFMT != kind || stat.Nlink != 1 {
		return stat, fmt.Errorf("unsafe private file %s/%s: expected owner, mode 0600, type and one link", d.Path, name)
	}
	return stat, nil
}

func (d *Dir) File(name string, flags int) (*os.File, error) {
	if flags&unix.O_TRUNC != 0 {
		return nil, errors.New("validate before truncating a private file")
	}
	if !validName(name) {
		return nil, errors.New("invalid private filename")
	}
	fd, err := unix.Openat(d.FD(), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.Path, name))
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		f.Close()
		return nil, err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0777 != 0600 || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("unsafe private file %s", f.Name())
	}
	return f, nil
}

func (d *Dir) Read(name string, limit int64) ([]byte, error) {
	f, err := d.File(name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("private file %s exceeds %d bytes", name, limit)
	}
	return b, err
}

func (d *Dir) AtomicWrite(name string, data []byte) error {
	if !validName(name) {
		return errors.New("invalid private filename")
	}
	if _, err := d.Stat(name, unix.S_IFREG); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temp := ".tmp-" + hex.EncodeToString(random[:])
	f, err := d.File(temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(d.FD(), temp, 0)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(d.FD(), temp, d.FD(), name); err != nil {
		return err
	}
	return d.file.Sync()
}

func (d *Dir) RemoveSocket(name string) error {
	if _, err := d.Stat(name, unix.S_IFSOCK); err != nil {
		return err
	}
	return unix.Unlinkat(d.FD(), name, 0)
}

func (d *Dir) Names() ([]string, error) {
	// A fresh open gives an independent directory iteration offset.
	fd, err := unix.Openat(d.FD(), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), d.Path)
	defer f.Close()
	return f.Readdirnames(-1)
}
