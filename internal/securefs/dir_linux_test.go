//go:build linux

package securefs

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRejectSymlinksPermissionsAndHardlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "symlink")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(link, "child"), true); err == nil {
		t.Fatal("followed directory symlink")
	}
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(public, false); err == nil {
		t.Fatal("accepted nonprivate directory")
	}
	writable := filepath.Join(root, "writable")
	if err := os.Mkdir(writable, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(writable, "child"), true); err == nil {
		t.Fatal("accepted writable ancestor")
	}
	dir, err := Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	target := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(target, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "bad")); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.File("bad", unix.O_RDWR|unix.O_CREAT); err == nil {
		t.Fatal("followed file symlink")
	}
	if err := dir.AtomicWrite("bad", []byte("unsafe")); err == nil {
		t.Fatal("replaced symlink state")
	}
	if err := os.Link(target, filepath.Join(root, "hard")); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.File("hard", unix.O_RDWR); err == nil {
		t.Fatal("accepted hardlink")
	}
	if err := dir.AtomicWrite("hard", []byte("unsafe")); err == nil {
		t.Fatal("replaced hardlinked state")
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "safe" {
		t.Fatal("modified unrelated file")
	}
	if _, err := dir.File("../escape", unix.O_CREAT|unix.O_WRONLY); err == nil {
		t.Fatal("accepted traversal")
	}
}

func TestAtomicWriteAndDirectoryCapability(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(parent, "private")
	d, err := Open(original, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	renamed := filepath.Join(parent, "renamed")
	if err := os.Rename(original, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), original); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"first", "second"} {
		if err := d.AtomicWrite("state.json", []byte(value)); err != nil {
			t.Fatal(err)
		}
		b, err := d.Read("state.json", 100)
		if err != nil || string(b) != value {
			t.Fatal(string(b), err)
		}
	}
	if _, err := d.Stat("state.json", unix.S_IFREG); err != nil {
		t.Fatal(err)
	}
	names, err := d.Names()
	if err != nil || len(names) != 1 || names[0] != "state.json" {
		t.Fatal(names, err)
	}
	if _, err := d.Read("state.json", 1); err == nil {
		t.Fatal("unbounded private read")
	}
	if _, err := d.File("state.json", unix.O_WRONLY|unix.O_TRUNC); err == nil {
		t.Fatal("truncated before validating")
	}
}
