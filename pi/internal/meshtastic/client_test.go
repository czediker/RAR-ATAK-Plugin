package meshtastic

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("INFO | boot log line\r\n")
	buf.Write([]byte{start1, start1}) // stray START1 before a real frame
	if err := WriteFrame(&buf, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf.Write([]byte{start1, start2, 0x7f, 0xff}) // bad length → resync
	buf.WriteString("more noise\n")
	if err := WriteFrame(&buf, []byte{}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&buf, bytes.Repeat([]byte{start1}, 10)); err != nil {
		t.Fatal(err)
	}

	var noise []string
	fr := NewFrameReader(&buf)
	fr.Noise = func(s string) { noise = append(noise, s) }
	for _, want := range [][]byte{[]byte("hello"), {}, bytes.Repeat([]byte{start1}, 10)} {
		got, err := fr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	if _, err := fr.Next(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
	if len(noise) == 0 || noise[0] != "INFO | boot log line" {
		t.Errorf("noise = %q", noise)
	}
	if err := WriteFrame(io.Discard, make([]byte, MaxFrame+1)); err != ErrFrameTooLarge {
		t.Errorf("oversize err = %v", err)
	}
}

// fakeRadio emulates the firmware side of the stream API on a net.Pipe.
type fakeRadio struct {
	t       *testing.T
	rw      io.ReadWriter
	closer  io.Closer
	node    uint32
	hop     uint32
	mu      sync.Mutex
	sent    []*meshpb.MeshPacket
	hbs     int
	configs int
	byes    int
}

func (f *fakeRadio) serve() {
	fr := NewFrameReader(f.rw)
	for {
		b, err := fr.Next()
		if err != nil {
			return
		}
		var msg meshpb.ToRadio
		if err := proto.Unmarshal(b, &msg); err != nil {
			f.t.Errorf("bad ToRadio: %v", err)
			return
		}
		switch v := msg.GetPayloadVariant().(type) {
		case *meshpb.ToRadio_WantConfigId:
			f.mu.Lock()
			f.configs++
			f.mu.Unlock()
			f.send(&meshpb.FromRadio{PayloadVariant: &meshpb.FromRadio_MyInfo{MyInfo: &meshpb.MyNodeInfo{MyNodeNum: f.node}}})
			f.send(&meshpb.FromRadio{PayloadVariant: &meshpb.FromRadio_Config{Config: &meshpb.Config{
				PayloadVariant: &meshpb.Config_Lora{Lora: &meshpb.Config_LoRaConfig{HopLimit: f.hop}},
			}}})
			f.send(&meshpb.FromRadio{PayloadVariant: &meshpb.FromRadio_ConfigCompleteId{ConfigCompleteId: v.WantConfigId}})
		case *meshpb.ToRadio_Packet:
			f.mu.Lock()
			f.sent = append(f.sent, v.Packet)
			f.mu.Unlock()
			// Firmware acknowledges every client packet with a QueueStatus.
			f.send(&meshpb.FromRadio{PayloadVariant: &meshpb.FromRadio_QueueStatus{QueueStatus: &meshpb.QueueStatus{
				Free: 15, Maxlen: 16, MeshPacketId: v.Packet.GetId(),
			}}})
		case *meshpb.ToRadio_Heartbeat:
			f.mu.Lock()
			f.hbs++
			f.mu.Unlock()
		case *meshpb.ToRadio_Disconnect:
			f.mu.Lock()
			f.byes++
			f.mu.Unlock()
		}
	}
}

func (f *fakeRadio) send(m *meshpb.FromRadio) {
	b, _ := proto.Marshal(m)
	_ = WriteFrame(f.rw, b)
}

func (f *fakeRadio) deliver(from, id uint32, port meshpb.PortNum, payload []byte) {
	f.send(&meshpb.FromRadio{PayloadVariant: &meshpb.FromRadio_Packet{Packet: &meshpb.MeshPacket{
		From: from, To: Broadcast, Id: id,
		PayloadVariant: &meshpb.MeshPacket_Decoded{Decoded: &meshpb.Data{Portnum: port, Payload: payload}},
	}}})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTestClient(t *testing.T, cfg Config) (*Client, chan *fakeRadio, context.CancelFunc) {
	radios := make(chan *fakeRadio, 4)
	open := func() (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		fr := &fakeRadio{t: t, rw: b, closer: b, node: 0xabcd1234, hop: 5}
		go fr.serve()
		radios <- fr
		return a, nil
	}
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.ReconnectDelay = 10 * time.Millisecond
	c := New(cfg, open)
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	return c, radios, cancel
}

func TestClientHandshakeSendReceive(t *testing.T) {
	c, radios, cancel := newTestClient(t, Config{Channel: 2, HeartbeatInterval: 20 * time.Millisecond})
	defer cancel()
	radio := <-radios

	if _, err := c.SendData(meshpb.PortNum_ATAK_PLUGIN, []byte("x")); err != ErrNotConnected && !c.Status().Connected {
		t.Fatalf("expected ErrNotConnected before handshake, got %v", err)
	}
	waitFor(t, "handshake", func() bool { return c.Status().Connected })
	st := c.Status()
	if st.NodeNum != 0xabcd1234 || st.DeviceHopLimit != 5 {
		t.Fatalf("status = %+v", st)
	}

	id, err := c.SendData(meshpb.PortNum_ATAK_PLUGIN, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "packet at radio", func() bool { radio.mu.Lock(); defer radio.mu.Unlock(); return len(radio.sent) == 1 })
	p := radio.sent[0]
	if p.GetTo() != Broadcast || p.GetChannel() != 2 || p.GetHopLimit() != 5 || p.GetId() != id {
		t.Errorf("sent packet = %v", p)
	}
	if p.GetDecoded().GetPortnum() != meshpb.PortNum_ATAK_PLUGIN || string(p.GetDecoded().GetPayload()) != "payload" {
		t.Errorf("sent data = %v", p.GetDecoded())
	}

	// Our own packets are ignored; others are delivered.
	radio.deliver(0xabcd1234, 1, meshpb.PortNum_ATAK_PLUGIN, []byte("self"))
	radio.deliver(0x11112222, 2, meshpb.PortNum_ATAK_PLUGIN, []byte("other"))
	select {
	case got := <-c.Packets():
		if got.GetFrom() != 0x11112222 || string(got.GetDecoded().GetPayload()) != "other" {
			t.Errorf("received %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no packet received")
	}

	waitFor(t, "heartbeat", func() bool { radio.mu.Lock(); defer radio.mu.Unlock(); return radio.hbs > 0 })
}

func TestClientConfiguredHopLimitOverridesDevice(t *testing.T) {
	c, radios, cancel := newTestClient(t, Config{HopLimit: 2})
	defer cancel()
	radio := <-radios
	waitFor(t, "handshake", func() bool { return c.Status().Connected })
	if _, err := c.SendData(meshpb.PortNum_ATAK_PLUGIN, []byte("x")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "packet", func() bool { radio.mu.Lock(); defer radio.mu.Unlock(); return len(radio.sent) == 1 })
	if radio.sent[0].GetHopLimit() != 2 {
		t.Errorf("hop limit = %d", radio.sent[0].GetHopLimit())
	}
}

func TestClientReconnectsAndReconfiguresAfterReboot(t *testing.T) {
	c, radios, cancel := newTestClient(t, Config{})
	defer cancel()
	radio := <-radios
	waitFor(t, "handshake", func() bool { return c.Status().Connected })

	// Firmware reboot notice → client asks for config again.
	radio.send(&meshpb.FromRadio{PayloadVariant: &meshpb.FromRadio_Rebooted{Rebooted: true}})
	waitFor(t, "second config", func() bool { radio.mu.Lock(); defer radio.mu.Unlock(); return radio.configs >= 2 })
	waitFor(t, "reconnected", func() bool { return c.Status().Connected })

	// Link drop → client reopens the port.
	radio.closer.Close()
	var second *fakeRadio
	select {
	case second = <-radios:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not reconnect")
	}
	waitFor(t, "handshake on new link", func() bool { return c.Status().Connected })
	second.mu.Lock()
	defer second.mu.Unlock()
	if second.configs != 1 {
		t.Errorf("configs on new link = %d", second.configs)
	}
}

func TestNodeID(t *testing.T) {
	if got := NodeID(0xa1b2c3); got != "!00a1b2c3" {
		t.Errorf("NodeID = %q", got)
	}
}

func TestOnFromRadioSeesQueueStatus(t *testing.T) {
	acks := make(chan uint32, 4)
	c, radios, cancel := newTestClient(t, Config{OnFromRadio: func(m *meshpb.FromRadio) {
		if qs := m.GetQueueStatus(); qs != nil {
			acks <- qs.GetMeshPacketId()
		}
	}})
	defer cancel()
	<-radios
	waitFor(t, "handshake", func() bool { return c.Status().Connected })
	id, err := c.SendData(meshpb.PortNum_TEXT_MESSAGE_APP, []byte("1"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-acks:
		if got != id {
			t.Errorf("ack for %x, sent %x", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no QueueStatus seen by OnFromRadio")
	}
}

func TestClientTellsRadioWhenShuttingDown(t *testing.T) {
	c, radios, cancel := newTestClient(t, Config{})
	radio := <-radios
	waitFor(t, "handshake", func() bool { return c.Status().Connected })
	cancel()
	waitFor(t, "disconnect at radio", func() bool { radio.mu.Lock(); defer radio.mu.Unlock(); return radio.byes == 1 })
}

func TestClientCountsWhatArrives(t *testing.T) {
	c, radios, cancel := newTestClient(t, Config{})
	defer cancel()
	<-radios
	waitFor(t, "handshake", func() bool { return c.Status().Connected })
	// MyInfo, Config and ConfigCompleteId.
	if st := c.Status(); st.RxFrames != 3 || st.RxBytes < 3*4 {
		t.Errorf("rx bytes/frames = %d/%d", st.RxBytes, st.RxFrames)
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A radio that hears the Pi but whose replies never arrive (the RAK's TX
// line is dead): the warning must say nothing at all came back.
func TestClientExplainsSilentRadio(t *testing.T) {
	var logs syncBuffer
	open := func() (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		go io.Copy(io.Discard, b)
		return a, nil
	}
	c := New(Config{ConfigTimeout: 30 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(&logs, nil))}, open)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "warning", func() bool { return strings.Contains(logs.String(), "no config response") })
	out := logs.String()
	for _, want := range []string{"received_bytes=0", "api_frames=0", "nothing arrived from the radio", "NOT_PRESENT"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning lacks %q:\n%s", want, out)
		}
	}
	if st := c.Status(); st.Connected || st.RxBytes != 0 || !strings.HasPrefix(st.LastError, "no config response from radio (0 bytes") {
		t.Errorf("status = %+v", st)
	}
}

func TestLinkHint(t *testing.T) {
	silent, garbage, lossy := LinkHint(0, 0), LinkHint(500, 0), LinkHint(500, 2)
	if silent == garbage || garbage == lossy || silent == lossy {
		t.Fatal("hints are not distinct")
	}
	if !strings.Contains(silent, "pin 10") || !strings.Contains(garbage, "PROTO") || !strings.Contains(lossy, "other program") {
		t.Errorf("hints:\n%s\n%s\n%s", silent, garbage, lossy)
	}
}
