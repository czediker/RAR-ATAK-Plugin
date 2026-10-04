// Package uartcheck looks for things on the Pi that interfere with the
// radio's serial port: a Linux console on it, or another program holding it
// open (a login prompt, gpsd, a second bridge).
package uartcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Check describes each problem found for device (e.g. /dev/ttyAMA0). It
// reads /proc and /dev, skips anything it cannot read, and never reports
// the calling process.
func Check(device string) []string {
	return check("/proc", "/dev", device, os.Getpid())
}

func check(proc, dev, device string, self int) []string {
	real := resolve(device)
	var problems []string
	if b, err := os.ReadFile(filepath.Join(proc, "cmdline")); err == nil {
		for _, c := range consoles(string(b), dev, real) {
			problems = append(problems, fmt.Sprintf(
				"the Linux console is on the radio's serial port (%s in the kernel command line): delete it from cmdline.txt and reboot", c))
		}
	}
	for _, h := range holders(proc, real, self) {
		problems = append(problems, fmt.Sprintf(
			"process %d (%s) has %s open and will take data meant for the bridge", h.pid, h.name, real))
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

type holder struct {
	pid  int
	name string
}

// holders lists the processes, other than self, with real open.
func holders(proc, real string, self int) []holder {
	dirs, _ := filepath.Glob(filepath.Join(proc, "[0-9]*"))
	var out []holder
	for _, d := range dirs {
		pid, err := strconv.Atoi(filepath.Base(d))
		if err != nil || pid == self {
			continue
		}
		fds, _ := os.ReadDir(filepath.Join(d, "fd"))
		for _, fd := range fds {
			if target, err := os.Readlink(filepath.Join(d, "fd", fd.Name())); err == nil && target == real {
				comm, _ := os.ReadFile(filepath.Join(d, "comm"))
				out = append(out, holder{pid, strings.TrimSpace(string(comm))})
				break
			}
		}
	}
	return out
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
