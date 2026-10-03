package meshtastic

import (
	"io"

	"go.bug.st/serial"
)

// SerialOpener returns an Opener for a serial device (8N1, no flow control).
func SerialOpener(device string, baud int) Opener {
	return func() (io.ReadWriteCloser, error) {
		return serial.Open(device, &serial.Mode{
			BaudRate: baud,
			DataBits: 8,
			Parity:   serial.NoParity,
			StopBits: serial.OneStopBit,
		})
	}
}
