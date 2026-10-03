package meshtastic

import (
	"errors"
	"fmt"
	"io"
	"os"

	"go.bug.st/serial"
	"golang.org/x/sys/unix"
)

// ErrPortBusy is returned when another program (normally rar-bridge) holds
// the serial port.
var ErrPortBusy = errors.New("serial port is in use by another program")

// SerialOpener returns an Opener for a serial device (8N1, no flow control).
//
// The port is also locked with flock(2) so two programs (for example
// rar-bridge and rar-meshtest) never read the same UART at once: the
// kernel's exclusive-open mode does not apply to root.
func SerialOpener(device string, baud int) Opener {
	return func() (io.ReadWriteCloser, error) {
		lock, err := os.OpenFile(device, os.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			lock.Close()
			if errors.Is(err, unix.EWOULDBLOCK) {
				return nil, fmt.Errorf("%s: %w", device, ErrPortBusy)
			}
			return nil, fmt.Errorf("lock %s: %w", device, err)
		}
		p, err := serial.Open(device, &serial.Mode{
			BaudRate: baud,
			DataBits: 8,
			Parity:   serial.NoParity,
			StopBits: serial.OneStopBit,
		})
		if err != nil {
			lock.Close()
			return nil, err
		}
		return &lockedPort{Port: p, lock: lock}, nil
	}
}

type lockedPort struct {
	serial.Port
	lock *os.File
}

// Close closes the port and releases the lock.
func (l *lockedPort) Close() error {
	err := l.Port.Close()
	l.lock.Close()
	return err
}
