package halowseen

import (
	"errors"
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

func TestListenerErrorAndLastHeard(t *testing.T) {
	tr := NewTracker(time.Minute)
	if tr.ListenerError() != "" {
		t.Fatal("no error expected initially")
	}
	tr.setListenerError(SAGroup, errors.New("no such interface"))
	tr.setListenerError(ChatGroup, errors.New("boom"))
	if got := tr.ListenerError(); got != ChatGroup+": boom; "+SAGroup+": no such interface" {
		t.Errorf("ListenerError = %q", got)
	}
	tr.setListenerError(ChatGroup, nil)
	tr.setListenerError(SAGroup, nil)
	if tr.ListenerError() != "" {
		t.Error("errors should clear")
	}
	at := time.Unix(5, 0)
	tr.Mark("u", at)
	if last, ok := tr.LastHeard("u"); !ok || !last.Equal(at) {
		t.Errorf("LastHeard = %v %v", last, ok)
	}
	if _, ok := tr.LastHeard("nobody"); ok {
		t.Error("unknown uid")
	}
}
