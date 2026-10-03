package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
	"github.com/czediker/rar-atak-plugin/pi/internal/meshtastic"
)

// fakeRadio acknowledges each packet the way the firmware does, via a
// QueueStatus carrying the packet ID.
type fakeRadio struct {
	connected bool
	info      *radioInfo
	reject    map[int]int32 // message number → error code
	noAck     map[int]bool
	sendErr   map[int]error
	sent      []string
	ports     []meshpb.PortNum
}

func (f *fakeRadio) Status() meshtastic.Status {
	return meshtastic.Status{Connected: f.connected, NodeNum: 0xa1b2c3d4, DeviceHopLimit: 3, LastError: "open /dev/ttyAMA0: no such file or directory"}
}

func (f *fakeRadio) SendData(port meshpb.PortNum, payload []byte) (uint32, error) {
	n := len(f.sent) + 1
	if err := f.sendErr[n]; err != nil {
		f.sent = append(f.sent, "")
		return 0, err
	}
	f.sent = append(f.sent, string(payload))
	f.ports = append(f.ports, port)
	id := uint32(1000 + n)
	if !f.noAck[n] {
		f.info.observe(&meshpb.FromRadio{PayloadVariant: &meshpb.FromRadio_QueueStatus{QueueStatus: &meshpb.QueueStatus{
			Res: f.reject[n], Free: 15, Maxlen: 16, MeshPacketId: id,
		}}})
	}
	return id, nil
}

func fastOptions() options {
	return options{Count: 10, Interval: time.Millisecond, ConnectTimeout: 200 * time.Millisecond, AckTimeout: 100 * time.Millisecond, Drain: time.Millisecond}
}

func newFake() (*fakeRadio, *radioInfo) {
	info := newRadioInfo()
	info.observe(&meshpb.FromRadio{PayloadVariant: &meshpb.FromRadio_Metadata{Metadata: &meshpb.DeviceMetadata{FirmwareVersion: "2.7.15"}}})
	info.observe(&meshpb.FromRadio{PayloadVariant: &meshpb.FromRadio_Channel{Channel: &meshpb.Channel{
		Index: 0, Role: meshpb.Channel_PRIMARY, Settings: &meshpb.ChannelSettings{Name: "TAK"},
	}}})
	return &fakeRadio{connected: true, info: info}, info
}

func TestSendsTenNumberedTextMessages(t *testing.T) {
	r, info := newFake()
	var out strings.Builder
	if code := run(context.Background(), r, info, fastOptions(), &out); code != 0 {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	want := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
	if strings.Join(r.sent, ",") != strings.Join(want, ",") {
		t.Errorf("sent %q", r.sent)
	}
	for _, p := range r.ports {
		if p != meshpb.PortNum_TEXT_MESSAGE_APP {
			t.Errorf("port %v", p)
		}
	}
	o := out.String()
	for _, s := range []string{"node !a1b2c3d4", "firmware 2.7.15", "channel 0 (TAK, PRIMARY)", `[10/10] "10" accepted`, "PASS: all 10 messages"} {
		if !strings.Contains(o, s) {
			t.Errorf("output missing %q:\n%s", s, o)
		}
	}
}

func TestReportsRejectedAndUnconfirmed(t *testing.T) {
	r, info := newFake()
	r.reject = map[int]int32{3: 32}
	r.noAck = map[int]bool{5: true}
	r.sendErr = map[int]error{7: errors.New("meshtastic: radio not connected")}
	var out strings.Builder
	if code := run(context.Background(), r, info, fastOptions(), &out); code != 1 {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	o := out.String()
	for _, s := range []string{`"3" REJECTED by the radio (error 32`, `"5" sent (id 000003ed), but the radio did not confirm it`, `"7" NOT SENT: meshtastic: radio not connected`, "FAIL: 7 of 10"} {
		if !strings.Contains(o, s) {
			t.Errorf("output missing %q:\n%s", s, o)
		}
	}
	if len(r.sent) != 10 {
		t.Errorf("should still attempt all 10, attempted %d", len(r.sent))
	}
}

func TestNoAnswerFromRadio(t *testing.T) {
	r, info := newFake()
	r.connected = false
	var out strings.Builder
	if code := run(context.Background(), r, info, fastOptions(), &out); code != 2 {
		t.Fatalf("exit %d", code)
	}
	o := out.String()
	if !strings.Contains(o, "FAIL: no answer from the radio") || !strings.Contains(o, "no such file or directory") || !strings.Contains(o, "PROTO mode") {
		t.Errorf("output:\n%s", o)
	}
	if len(r.sent) != 0 {
		t.Error("nothing should be sent without a connection")
	}
}

func TestInterrupted(t *testing.T) {
	r, info := newFake()
	ctx, cancel := context.WithCancel(context.Background())
	opt := fastOptions()
	opt.Interval = time.Hour
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	var out strings.Builder
	if code := run(ctx, r, info, opt, &out); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if len(r.sent) > 2 {
		t.Errorf("kept sending after interrupt: %d", len(r.sent))
	}
}
