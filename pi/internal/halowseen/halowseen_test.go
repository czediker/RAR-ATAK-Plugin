package halowseen

import (
	"slices"
	"testing"
	"time"
)

func TestTracker(t *testing.T) {
	tr := NewTracker(time.Minute)
	t0 := time.Unix(1000, 0)
	tr.Observe([]byte(`<event version="2.0" uid="ANDROID-a" type="a-f-G-U-C" time="2026-10-03T00:00:00Z"><point lat="1" lon="2"/><detail/></event>`), t0)
	tr.Observe([]byte(`<event version="2.0" uid="GeoChat.ANDROID-b.All Chat Rooms.m" type="b-t-f"><point lat="1" lon="2"/><detail><__chat chatroom="All Chat Rooms" id="All Chat Rooms" senderCallsign="B"><chatgrp uid0="ANDROID-b" uid1="All Chat Rooms"/></__chat><remarks>x</remarks></detail></event>`), t0.Add(30*time.Second))
	tr.Observe([]byte(`garbage`), t0)
	tr.Mark("", t0)

	if !tr.Recently("ANDROID-a", t0.Add(time.Minute)) || !tr.Recently("ANDROID-b", t0.Add(time.Minute)) {
		t.Error("expected both recently seen")
	}
	if tr.Recently("ANDROID-a", t0.Add(61*time.Second)) {
		t.Error("ANDROID-a should have aged out")
	}
	if got := tr.UIDs(t0.Add(61 * time.Second)); !slices.Equal(got, []string{"ANDROID-b"}) {
		t.Errorf("UIDs = %v", got)
	}
	if tr.Recently("nobody", t0) {
		t.Error("unknown uid")
	}
}
