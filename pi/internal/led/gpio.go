package led

import (
	"fmt"
	"log/slog"

	"github.com/warthog618/go-gpiocdev"
)

// Driver sets the physical LED.
type Driver interface {
	Set(Color) error
	Close() error
}

// GPIO drives a discrete RGB LED on three GPIO lines through the Linux GPIO
// character device.
type GPIO struct {
	lines *gpiocdev.Lines
}

// OpenGPIO requests the red, green and blue lines on chip (e.g.
// "gpiochip0"). activeLow must be true for a common-anode LED wired
// directly to the pins (pin low = LED on), and false when the cathodes are
// switched through transistors (pin high = LED on).
func OpenGPIO(chip string, red, green, blue int, activeLow bool) (*GPIO, error) {
	opts := []gpiocdev.LineReqOption{gpiocdev.WithConsumer("rar-led"), gpiocdev.AsOutput(0, 0, 0)}
	if activeLow {
		opts = append(opts, gpiocdev.AsActiveLow)
	}
	l, err := gpiocdev.RequestLines(chip, []int{red, green, blue}, opts...)
	if err != nil {
		return nil, fmt.Errorf("request GPIO %d/%d/%d on %s: %w", red, green, blue, chip, err)
	}
	return &GPIO{lines: l}, nil
}

// Set shows c.
func (g *GPIO) Set(c Color) error {
	return g.lines.SetValues([]int{b2i(c.R), b2i(c.G), b2i(c.B)})
}

// Close turns the LED off and releases the lines.
func (g *GPIO) Close() error {
	_ = g.Set(Off)
	return g.lines.Close()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// LogDriver logs colors instead of driving hardware (for -dry-run).
type LogDriver struct{ Log *slog.Logger }

// Set logs c.
func (d LogDriver) Set(c Color) error {
	d.Log.Info("led", "color", c.String())
	return nil
}

// Close does nothing.
func (LogDriver) Close() error { return nil }
