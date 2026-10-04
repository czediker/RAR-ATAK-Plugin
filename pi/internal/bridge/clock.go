package bridge

import "time"

// eudClock tracks the EUD's clock from the timestamps on the plugin's CoT.
//
// ATAK orders chat messages by the event time and judges staleness by it,
// so CoT handed to the EUD must carry the EUD's time. The Pi has no
// battery-backed clock and, without internet, often no time server either,
// so its own clock can be far off.
//
// Each sample predicts the EUD's current time as the event time plus the
// time elapsed since it arrived (measured on the monotonic clock, so a step
// of the Pi's wall clock does not disturb it). An event is never generated
// after it arrives, only before, so the largest prediction is the closest:
// it comes from the freshest event.
type eudClock struct {
	samples []clockSample // oldest first
}

type clockSample struct {
	remote time.Time // the event's time attribute (EUD clock)
	at     time.Time // when it arrived (this host's clock)
}

const (
	clockSamples = 16
	// clockWindow is how long a sample is trusted. The newest sample is
	// always kept, so the estimate survives a quiet EUD.
	clockWindow = 10 * time.Minute
)

// observe records an event generated at remote (EUD time) that arrived at
// local time at.
func (c *eudClock) observe(remote, at time.Time) {
	if remote.IsZero() {
		return
	}
	c.samples = append(c.samples, clockSample{remote: remote, at: at})
	if len(c.samples) > clockSamples {
		c.samples = c.samples[len(c.samples)-clockSamples:]
	}
}

// now returns the EUD's current time, given this host's time local. Before
// any sample it returns local.
func (c *eudClock) now(local time.Time) time.Time {
	if len(c.samples) == 0 {
		return local
	}
	newest := c.samples[len(c.samples)-1]
	best := newest.remote.Add(local.Sub(newest.at))
	keep := c.samples[:0]
	for _, s := range c.samples {
		if local.Sub(s.at) > clockWindow && s != newest {
			continue
		}
		keep = append(keep, s)
		if p := s.remote.Add(local.Sub(s.at)); p.After(best) {
			best = p
		}
	}
	c.samples = keep
	return best
}

// offset is how far the EUD's clock is ahead of this host's (negative when
// behind), and false before any sample.
func (c *eudClock) offset(local time.Time) (time.Duration, bool) {
	if len(c.samples) == 0 {
		return 0, false
	}
	return c.now(local).Sub(local), true
}
