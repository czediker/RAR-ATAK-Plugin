package uartcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSystem builds a /dev with ttyAMA0 and serial0 -> ttyAMA0, and a /proc
// with the given command line.
func fakeSystem(t *testing.T, cmdline string) (proc, dev, tty string) {
	t.Helper()
	root := resolve(t.TempDir())
	proc, dev = filepath.Join(root, "proc"), filepath.Join(root, "dev")
	for _, d := range []string{proc, dev} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tty = filepath.Join(dev, "ttyAMA0")
	write(t, tty, "")
	write(t, filepath.Join(dev, "tty1"), "")
	if err := os.Symlink("ttyAMA0", filepath.Join(dev, "serial0")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(proc, "cmdline"), cmdline+"\n")
	return proc, dev, tty
}

// process adds /proc/<pid> with fds pointing at targets.
func process(t *testing.T, proc, pid, comm string, targets ...string) {
	t.Helper()
	fd := filepath.Join(proc, pid, "fd")
	if err := os.MkdirAll(fd, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(proc, pid, "comm"), comm+"\n")
	for i, target := range targets {
		if err := os.Symlink(target, filepath.Join(fd, string(rune('0'+i)))); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCleanSystem(t *testing.T) {
	proc, dev, tty := fakeSystem(t, "console=tty1 root=PARTUUID=abcd-02 rootfstype=squashfs,ext4 rootwait")
	process(t, proc, "100", "procd", "/dev/null", filepath.Join(dev, "tty1"))
	process(t, proc, "300", "rar-bridge", tty) // the caller itself
	if got := check(proc, dev, filepath.Join(dev, "serial0"), 300); len(got) != 0 {
		t.Errorf("problems on a clean system: %q", got)
	}
}

func TestFindsConsoleAndHolders(t *testing.T) {
	proc, dev, tty := fakeSystem(t, "console=tty1 console=serial0,115200 root=PARTUUID=abcd-02 rootwait")
	process(t, proc, "100", "getty", "/dev/null", tty)
	process(t, proc, "200", "gpsd", tty)
	process(t, proc, "300", "rar-meshtest", tty)
	got := check(proc, dev, tty, 300)
	if len(got) != 3 {
		t.Fatalf("problems = %q", got)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{"console=serial0,115200", "process 100 (getty)", "process 200 (gpsd)"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "rar-meshtest") {
		t.Errorf("reported the caller itself:\n%s", all)
	}
}

func TestConsoleByDeviceName(t *testing.T) {
	_, dev, tty := fakeSystem(t, "")
	got := consoles("console=ttyAMA0,115200n8 console=tty1 console=", dev, tty)
	if len(got) != 1 || got[0] != "console=ttyAMA0,115200n8" {
		t.Errorf("consoles = %q", got)
	}
}
