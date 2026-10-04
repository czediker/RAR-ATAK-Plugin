package bridge

import (
	"testing"
	"time"
)

func TestEUDClock(t *testing.T) {
	var c eudClock
	if got := c.now(t0); !got.Equal(t0) {
		t.Errorf("no samples: %v", got)
	}
	if _, ok := c.offset(t0); ok {
		t.Error("offset known without samples")
	}

	// The EUD is two hours ahead of this host.
	ahead := 2 * time.Hour
	c.observe(t0.Add(ahead), t0)
	if got := c.now(at(10)); !got.Equal(at(10).Add(ahead)) {
		t.Errorf("now = %v", got)
	}
	// An event generated five minutes before it arrived (a cached position)
	// must not pull the estimate back.
	c.observe(at(20).Add(ahead-5*time.Minute), at(20))
	if off, _ := c.offset(at(30)); off != ahead {
		t.Errorf("offset after stale event = %v", off)
	}
	// Zero times are ignored.
	c.observe(time.Time{}, at(40))
	if off, _ := c.offset(at(40)); off != ahead {
		t.Errorf("offset after zero time = %v", off)
	}

	// Samples age out after clockWindow; the newest one is always kept, so
	// a clock change on the EUD takes over.
	later := at(20).Add(clockWindow + time.Minute)
	c.observe(later.Add(time.Hour), later)
	if off, _ := c.offset(later); off != time.Hour {
		t.Errorf("offset after window = %v", off)
	}
	if off, _ := c.offset(later.Add(2 * clockWindow)); off != time.Hour {
		t.Errorf("newest sample dropped: %v", off)
	}
}
