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
// "gpiochip0"). For a common-cathode LED with each anode on a pin through a
// resistor, activeLow is false (pin high = LED on). Set it for a
// common-anode LED (pin low = LED on). The red line is requested too so it
// is held off until something uses the red hook.
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
	_ = g.Set(Color{})
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
