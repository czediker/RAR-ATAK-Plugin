// Package uartcheck looks for things on the Pi that interfere with the
// radio's serial port: a Linux console or login on it, or another program
// holding it open (gpsd, a second bridge).
package uartcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Check describes each problem found for device (e.g. /dev/ttyAMA0). It
// reads /proc, /sys and /dev, skips anything it cannot read, and never
// reports the calling process.
func Check(device string) []string {
	real := resolve(device)
	var rdev uint64
	var st unix.Stat_t
	if unix.Stat(real, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFCHR {
		rdev = uint64(st.Rdev)
	}
	return check(system{proc: "/proc", sys: "/sys", dev: "/dev"}, real, rdev, os.Getpid())
}

type system struct{ proc, sys, dev string }

// check: real is the resolved device path, rdev its device number (0 if
// unknown).
func check(s system, real string, rdev uint64, self int) []string {
	var problems []string
	if b, err := os.ReadFile(filepath.Join(s.proc, "cmdline")); err == nil {
		for _, c := range consoles(string(b), s.dev, real) {
			problems = append(problems, fmt.Sprintf(
				"the Linux console is on the radio's serial port (%s in the kernel command line): "+
					"delete it from cmdline.txt on the boot partition and reboot", c))
		}
	}
	if len(problems) == 0 {
		// The kernel's own list catches a console set another way (device
		// tree, or serial0 rewritten by the Pi firmware).
		if b, err := os.ReadFile(filepath.Join(s.sys, "class/tty/console/active")); err == nil {
			for _, name := range strings.Fields(string(b)) {
				if resolve(filepath.Join(s.dev, name)) == real {
					problems = append(problems, fmt.Sprintf(
						"the Linux console is on the radio's serial port (%s is in /sys/class/tty/console/active): "+
							"remove its console= entry from cmdline.txt on the boot partition and reboot", name))
				}
			}
		}
	}
	for _, p := range processes(s.proc, real, rdev, self) {
		if p.terminal {
			problems = append(problems, fmt.Sprintf(
				"process %d (%s) uses %s as its terminal (a login console); "+
					"each time its session ends the kernel hangs up the port", p.pid, p.name, real))
		} else {
			msg := fmt.Sprintf("process %d (%s) has %s open and will take data meant for the bridge", p.pid, p.name, real)
			if p.name == "gpsd" {
				msg += "; gpsd is a GPS daemon, so a GPS receiver is probably wired to this port too " +
					"(the Seeed WM1302 Pi HAT's GPS uses the Pi's UART, pins 8/10): connect the RAK4631 by USB instead"
			}
			problems = append(problems, msg)
		}
	}
	return problems
}

// consoles returns the console= entries of a kernel command line that point
// at the device real (directly or through a link such as serial0).
func consoles(cmdline, dev, real string) []string {
	var out []string
	for _, f := range strings.Fields(cmdline) {
		name, ok := strings.CutPrefix(f, "console=")
		if !ok {
			continue
		}
		name, _, _ = strings.Cut(name, ",")
		if name != "" && resolve(filepath.Join(dev, name)) == real {
			out = append(out, f)
		}
	}
	return out
}

type process struct {
	pid  int
	name string
	// terminal: the device is the process's controlling terminal.
	terminal bool
}

// processes lists the processes, other than self, that have real open or
// use it as their controlling terminal (as a login started on /dev/console
// does).
func processes(proc, real string, rdev uint64, self int) []process {
	dirs, _ := filepath.Glob(filepath.Join(proc, "[0-9]*"))
	var out []process
	for _, d := range dirs {
		pid, err := strconv.Atoi(filepath.Base(d))
		if err != nil || pid == self {
			continue
		}
		terminal := rdev != 0 && ctty(d) == rdev
		if !terminal && !hasOpen(d, real) {
			continue
		}
		comm, _ := os.ReadFile(filepath.Join(d, "comm"))
		out = append(out, process{pid, strings.TrimSpace(string(comm)), terminal})
	}
	return out
}

func hasOpen(dir, real string) bool {
	fds, _ := os.ReadDir(filepath.Join(dir, "fd"))
	for _, fd := range fds {
		if target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name())); err == nil && target == real {
			return true
		}
	}
	return false
}

// ctty returns the device number of a process's controlling terminal, or 0.
func ctty(dir string) uint64 {
	b, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return 0
	}
	// pid (comm) state ppid pgrp session tty_nr ...; comm may contain spaces.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 5 {
		return 0
	}
	nr, err := strconv.ParseUint(f[4], 10, 32)
	if err != nil || nr == 0 {
		return 0
	}
	major := uint32(nr>>8) & 0xfff
	minor := uint32(nr&0xff) | uint32(nr>>12)&0xfff00
	return unix.Mkdev(major, minor)
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
