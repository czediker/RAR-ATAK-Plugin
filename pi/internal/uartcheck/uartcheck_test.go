package uartcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// ttyAMA0 is the PL011 UART's device number on a Pi (major 204, minor 64).
var ttyAMA0 = unix.Mkdev(204, 64)

// fakeSystem builds a /dev with ttyAMA0 and serial0 -> ttyAMA0, a /sys
// console list, and a /proc with the given command line.
func fakeSystem(t *testing.T, cmdline, active string) (s system, tty string) {
	t.Helper()
	root := resolve(t.TempDir())
	s = system{proc: filepath.Join(root, "proc"), sys: filepath.Join(root, "sys"), dev: filepath.Join(root, "dev")}
	for _, d := range []string{s.proc, s.dev, filepath.Join(s.sys, "class/tty/console")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tty = filepath.Join(s.dev, "ttyAMA0")
	write(t, tty, "")
	write(t, filepath.Join(s.dev, "tty1"), "")
	write(t, filepath.Join(s.dev, "console"), "")
	if err := os.Symlink("ttyAMA0", filepath.Join(s.dev, "serial0")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.proc, "cmdline"), cmdline+"\n")
	write(t, filepath.Join(s.sys, "class/tty/console/active"), active+"\n")
	return s, tty
}

// addProcess adds /proc/<pid> with a controlling terminal (tty_nr in Linux
// encoding, 0 = none) and fds pointing at targets.
func addProcess(t *testing.T, proc string, pid int, comm string, ttyNr uint64, targets ...string) {
	t.Helper()
	dir := filepath.Join(proc, fmt.Sprint(pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "comm"), comm+"\n")
	write(t, filepath.Join(dir, "stat"), fmt.Sprintf("%d (%s) S 1 %d %d %d -1 4194560 0 0\n", pid, comm, pid, pid, ttyNr))
	for i, target := range targets {
		if err := os.Symlink(target, filepath.Join(dir, "fd", fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
}

// ttyNr encodes a device number the way /proc/<pid>/stat does.
func ttyNr(dev uint64) uint64 {
	major, minor := uint64(unix.Major(dev)), uint64(unix.Minor(dev))
	return (minor & 0xff) | (major&0xfff)<<8 | (minor&^0xff)<<12
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCleanSystem(t *testing.T) {
	s, tty := fakeSystem(t, "console=tty1 root=PARTUUID=abcd-02 rootfstype=squashfs,ext4 rootwait", "tty1")
	addProcess(t, s.proc, 100, "askfirst", ttyNr(unix.Mkdev(4, 1)), filepath.Join(s.dev, "console")) // login on tty1
	addProcess(t, s.proc, 200, "procd", 0, "/dev/null")
	addProcess(t, s.proc, 300, "rar-bridge", 0, tty) // the caller itself
	if got := check(s, tty, ttyAMA0, 300); len(got) != 0 {
		t.Errorf("problems on a clean system: %q", got)
	}
}

func TestFindsConsoleAndHolders(t *testing.T) {
	s, tty := fakeSystem(t, "console=tty1 console=serial0,115200 root=PARTUUID=abcd-02 rootwait", "tty1 ttyAMA0")
	addProcess(t, s.proc, 100, "getty", 0, "/dev/null", tty)
	addProcess(t, s.proc, 200, "gpsd", 0, tty)
	addProcess(t, s.proc, 300, "rar-meshtest", 0, tty)
	got := check(s, tty, ttyAMA0, 300)
	if len(got) != 3 {
		t.Fatalf("problems = %q", got)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{"console=serial0,115200", "process 100 (getty) has", "process 200 (gpsd) has", "move the RAK4631 to uart2"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "rar-meshtest") {
		t.Errorf("reported the caller itself:\n%s", all)
	}
}

// A login started on /dev/console holds /dev/console, not /dev/ttyAMA0; only
// its controlling terminal and the kernel's console list give it away.
func TestFindsLoginOnConsole(t *testing.T) {
	s, tty := fakeSystem(t, "root=PARTUUID=abcd-02 rootwait", "ttyAMA0")
	addProcess(t, s.proc, 812, "askfirst", ttyNr(ttyAMA0), filepath.Join(s.dev, "console"))
	addProcess(t, s.proc, 813, "ash", ttyNr(ttyAMA0), filepath.Join(s.dev, "console"))
	all := strings.Join(check(s, tty, ttyAMA0, 1), "\n")
	for _, want := range []string{"ttyAMA0 is in /sys/class/tty/console/active", "process 812 (askfirst) uses", "process 813 (ash) uses", "hangs up the port"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
}

func TestConsoleByDeviceName(t *testing.T) {
	s, tty := fakeSystem(t, "", "")
	got := consoles("console=ttyAMA0,115200n8 console=tty1 console=", s.dev, tty)
	if len(got) != 1 || got[0] != "console=ttyAMA0,115200n8" {
		t.Errorf("consoles = %q", got)
	}
}

func TestCttyParsesCommWithSpaces(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "stat"), fmt.Sprintf("42 (odd (name) x) S 1 42 42 %d -1\n", ttyNr(ttyAMA0)))
	if got := ctty(dir); got != ttyAMA0 {
		t.Errorf("ctty = %d, want %d", got, ttyAMA0)
	}
}
