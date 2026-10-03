package meshtastic

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
)

// openPTY returns the master side of a new pseudo-terminal and the path of
// its slave, which stands in for /dev/ttyAMA0.
func openPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty support: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		t.Skipf("unlockpt: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		t.Skipf("ptsname: %v", err)
	}
	// Raw mode on the master so frame bytes pass through untouched.
	if tio, err := unix.IoctlGetTermios(int(m.Fd()), unix.TCGETS); err == nil {
		tio.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
		tio.Oflag &^= unix.OPOST
		tio.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		_ = unix.IoctlSetTermios(int(m.Fd()), unix.TCSETS, tio)
	}
	return m, fmt.Sprintf("/dev/pts/%d", n)
}

// TestSerialOpenerHandshake runs the real client over the real serial
// opener against a simulated radio on a pty.
func TestSerialOpenerHandshake(t *testing.T) {
	master, slave := openPTY(t)
	defer master.Close()

	radio := &fakeRadio{t: t, rw: master, closer: master, node: 0x12345678, hop: 4}
	go radio.serve()

	c := New(Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ReconnectDelay: 50 * time.Millisecond}, SerialOpener(slave, 115200))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "handshake over pty", func() bool { return c.Status().Connected })
	if c.Status().NodeNum != 0x12345678 {
		t.Errorf("node = %x", c.Status().NodeNum)
	}
	if _, err := c.SendData(meshpb.PortNum_ATAK_PLUGIN, []byte{0x94, 0xc3, 0x00, 0x0a}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "packet over pty", func() bool { radio.mu.Lock(); defer radio.mu.Unlock(); return len(radio.sent) == 1 })
}
