// Package meshtastic implements a minimal Meshtastic client for the
// protobuf stream API (the API exposed over USB serial, or over the RAK's
// UART when the Serial module runs in PROTO mode).
package meshtastic

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// Stream framing: 0x94 0xC3 <len hi> <len lo> <protobuf>.
const (
	start1 = 0x94
	start2 = 0xc3
	// MaxFrame is the firmware's MAX_TO_FROM_RADIO_SIZE.
	MaxFrame = 512
)

// ErrFrameTooLarge is returned when asked to write an oversized frame.
var ErrFrameTooLarge = errors.New("meshtastic: frame exceeds 512 bytes")

// WriteFrame writes one framed protobuf payload.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxFrame {
		return ErrFrameTooLarge
	}
	buf := make([]byte, 0, 4+len(payload))
	buf = append(buf, start1, start2, byte(len(payload)>>8), byte(len(payload)))
	buf = append(buf, payload...)
	_, err := w.Write(buf)
	return err
}

// FrameReader extracts framed payloads from a byte stream, skipping any
// bytes between frames (for example debug log text printed by the device).
type FrameReader struct {
	r *bufio.Reader
	// Noise, if set, receives each line of non-frame text seen on the
	// stream. Useful for diagnosing a device that is not in PROTO mode.
	Noise func(line string)
	line  []byte
}

// NewFrameReader wraps r.
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{r: bufio.NewReaderSize(r, 2*MaxFrame)}
}

// Next returns the next frame's payload. The returned slice is owned by the
// caller.
func (f *FrameReader) Next() ([]byte, error) {
	for {
		c, err := f.r.ReadByte()
		if err != nil {
			return nil, err
		}
		if c != start1 {
			f.noise(c)
			continue
		}
		// A byte that fails START2 may itself be the START1 of the real
		// frame (0x94 0x94 0xc3 ...), so loop instead of resetting.
		for {
			c, err = f.r.ReadByte()
			if err != nil {
				return nil, err
			}
			if c != start1 {
				break
			}
		}
		if c != start2 {
			f.noise(c)
			continue
		}
		var hdr [2]byte
		if _, err := io.ReadFull(f.r, hdr[:]); err != nil {
			return nil, err
		}
		n := int(hdr[0])<<8 | int(hdr[1])
		if n > MaxFrame {
			// Corrupt header: resynchronise on the next START1.
			f.flushNoise(fmt.Sprintf("<bad frame length %d>", n))
			continue
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(f.r, payload); err != nil {
			return nil, err
		}
		return payload, nil
	}
}

func (f *FrameReader) noise(c byte) {
	if f.Noise == nil {
		return
	}
	if c == '\n' || c == '\r' {
		if len(f.line) > 0 {
			f.flushNoise(string(f.line))
		}
		return
	}
	if len(f.line) < 512 {
		f.line = append(f.line, c)
	}
}

func (f *FrameReader) flushNoise(s string) {
	f.line = f.line[:0]
	if f.Noise != nil {
		f.Noise(s)
	}
}
