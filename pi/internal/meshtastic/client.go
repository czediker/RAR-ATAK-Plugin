package meshtastic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/czediker/rar-atak-plugin/pi/internal/meshpb"
)

// Broadcast is the Meshtastic broadcast node number.
const Broadcast = 0xffffffff

// defaultHopLimit is used when neither the config nor the device supplies one.
const defaultHopLimit = 3

// Nonces the firmware treats specially in want_config_id.
const (
	specialNonceOnlyConfig = 69420
	specialNonceOnlyNodes  = 69421
)

// ErrNotConnected is returned by SendData while the radio is not ready.
var ErrNotConnected = errors.New("meshtastic: radio not connected")

// Opener opens the byte stream to the radio (normally a serial port).
type Opener func() (io.ReadWriteCloser, error)

// Config tunes the client. Zero values select defaults.
type Config struct {
	// HopLimit for transmitted packets. 0 uses the hop limit configured on
	// the device (reported during the config handshake).
	HopLimit uint32
	// Channel index to transmit on.
	Channel uint32
	// HeartbeatInterval keeps the firmware's 15 minute API timeout from
	// expiring. Default 60s.
	HeartbeatInterval time.Duration
	// ConfigTimeout is how long to wait for the config handshake before
	// asking again. Default 20s.
	ConfigTimeout time.Duration
	// ReconnectDelay is the pause between connection attempts. Default 5s.
	ReconnectDelay time.Duration
	// RxBuffer is the received-packet channel size. Default 64.
	RxBuffer int
	Logger   *slog.Logger
	// OnFromRadio, if set, is called with every message received from the
	// radio (on the client's connection goroutine; it must not block).
	OnFromRadio func(*meshpb.FromRadio)
}

// Status describes the radio connection.
type Status struct {
	// Connected is true once the config handshake has completed on the
	// current connection.
	Connected bool
	NodeNum   uint32
	// DeviceHopLimit is the hop limit configured on the device.
	DeviceHopLimit uint32
	LastError      string
}

// NodeID formats a node number the way Meshtastic displays it.
func NodeID(n uint32) string { return fmt.Sprintf("!%08x", n) }

// Client talks to one Meshtastic radio over the stream API, reconnecting as
// needed. Received mesh packets are delivered on Packets().
type Client struct {
	cfg  Config
	open Opener
	log  *slog.Logger
	rx   chan *meshpb.MeshPacket

	mu     sync.Mutex
	conn   io.ReadWriteCloser
	status Status
	nonce  uint32

	writeMu sync.Mutex
}

// New creates a client. Call Run to start it.
func New(cfg Config, open Opener) *Client {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 60 * time.Second
	}
	if cfg.ConfigTimeout <= 0 {
		cfg.ConfigTimeout = 20 * time.Second
	}
	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = 5 * time.Second
	}
	if cfg.RxBuffer <= 0 {
		cfg.RxBuffer = 64
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Client{cfg: cfg, open: open, log: log.With("component", "meshtastic"), rx: make(chan *meshpb.MeshPacket, cfg.RxBuffer)}
}

// Packets returns the channel of packets received from the mesh.
func (c *Client) Packets() <-chan *meshpb.MeshPacket { return c.rx }

// Status returns a snapshot of the connection state.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Run connects and keeps the connection alive until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	for {
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		c.status.Connected = false
		if err != nil {
			c.status.LastError = err.Error()
		}
		c.mu.Unlock()
		c.log.Warn("radio connection lost; retrying", "err", err, "in", c.cfg.ReconnectDelay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.cfg.ReconnectDelay):
		}
	}
}

// SendData broadcasts a payload on the configured channel and returns the
// packet ID.
func (c *Client) SendData(port meshpb.PortNum, payload []byte) (uint32, error) {
	c.mu.Lock()
	conn, st := c.conn, c.status
	c.mu.Unlock()
	if conn == nil || !st.Connected {
		return 0, ErrNotConnected
	}
	hop := c.cfg.HopLimit
	if hop == 0 {
		hop = st.DeviceHopLimit
	}
	if hop == 0 {
		hop = defaultHopLimit
	}
	hop = min(hop, 7)

	id := randomID()
	msg := &meshpb.ToRadio{PayloadVariant: &meshpb.ToRadio_Packet{Packet: &meshpb.MeshPacket{
		To:       Broadcast,
		Channel:  c.cfg.Channel,
		Id:       id,
		HopLimit: hop,
		PayloadVariant: &meshpb.MeshPacket_Decoded{Decoded: &meshpb.Data{
			Portnum: port,
			Payload: payload,
		}},
	}}}
	if err := c.send(conn, msg); err != nil {
		return 0, err
	}
	return id, nil
}

func (c *Client) session(ctx context.Context) error {
	conn, err := c.open()
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-sctx.Done()
		conn.Close()
	}()

	c.mu.Lock()
	c.conn = conn
	c.status.Connected = false
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.status.Connected = false
		c.mu.Unlock()
	}()

	// Wake the device's stream parser, as the official clients do.
	wake := make([]byte, 32)
	for i := range wake {
		wake[i] = start2
	}
	if err := c.writeRaw(conn, wake); err != nil {
		return fmt.Errorf("wake: %w", err)
	}
	select {
	case <-sctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}

	frames := make(chan []byte, 16)
	readErr := make(chan error, 1)
	go func() {
		fr := NewFrameReader(conn)
		fr.Noise = func(line string) { c.log.Debug("radio console", "text", line) }
		for {
			f, err := fr.Next()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case frames <- f:
			case <-sctx.Done():
				return
			}
		}
	}()

	if err := c.requestConfig(conn); err != nil {
		return err
	}
	cfgTimer := time.NewTimer(c.cfg.ConfigTimeout)
	defer cfgTimer.Stop()
	heartbeat := time.NewTicker(c.cfg.HeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-sctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return fmt.Errorf("read: %w", err)
		case f := <-frames:
			reconfig, err := c.handleFrame(f)
			if err != nil {
				c.log.Debug("bad frame", "err", err, "len", len(f))
				continue
			}
			if reconfig {
				if err := c.requestConfig(conn); err != nil {
					return err
				}
				cfgTimer.Reset(c.cfg.ConfigTimeout)
			}
		case <-cfgTimer.C:
			if !c.Status().Connected {
				c.log.Warn("no config response from radio; check the Serial module is enabled in PROTO mode and the baud rate matches")
				if err := c.requestConfig(conn); err != nil {
					return err
				}
				cfgTimer.Reset(c.cfg.ConfigTimeout)
			}
		case <-heartbeat.C:
			if c.Status().Connected {
				hb := &meshpb.ToRadio{PayloadVariant: &meshpb.ToRadio_Heartbeat{Heartbeat: &meshpb.Heartbeat{Nonce: rand.Uint32()}}}
				if err := c.send(conn, hb); err != nil {
					return err
				}
			}
		}
	}
}

// handleFrame processes one FromRadio message. It reports whether the
// config handshake must be restarted.
func (c *Client) handleFrame(f []byte) (reconfig bool, err error) {
	var msg meshpb.FromRadio
	if err := proto.Unmarshal(f, &msg); err != nil {
		return false, err
	}
	if c.cfg.OnFromRadio != nil {
		c.cfg.OnFromRadio(&msg)
	}
	switch v := msg.GetPayloadVariant().(type) {
	case *meshpb.FromRadio_MyInfo:
		c.mu.Lock()
		c.status.NodeNum = v.MyInfo.GetMyNodeNum()
		c.mu.Unlock()
	case *meshpb.FromRadio_Config:
		if lora := v.Config.GetLora(); lora != nil {
			c.mu.Lock()
			c.status.DeviceHopLimit = lora.GetHopLimit()
			c.mu.Unlock()
		}
	case *meshpb.FromRadio_ConfigCompleteId:
		c.mu.Lock()
		match := v.ConfigCompleteId == c.nonce
		if match {
			c.status.Connected = true
			c.status.LastError = ""
		}
		st := c.status
		c.mu.Unlock()
		if match {
			c.log.Info("radio ready", "node", NodeID(st.NodeNum), "device_hop_limit", st.DeviceHopLimit)
		}
	case *meshpb.FromRadio_Rebooted:
		c.log.Warn("radio rebooted; reconfiguring")
		c.mu.Lock()
		c.status.Connected = false
		c.mu.Unlock()
		return true, nil
	case *meshpb.FromRadio_Packet:
		p := v.Packet
		if p == nil {
			return false, nil
		}
		if p.GetFrom() == c.Status().NodeNum {
			return false, nil
		}
		select {
		case c.rx <- p:
		default:
			c.log.Warn("receive buffer full; dropping packet", "from", NodeID(p.GetFrom()), "id", p.GetId())
		}
	case *meshpb.FromRadio_QueueStatus:
		qs := v.QueueStatus
		if qs.GetRes() != 0 {
			c.log.Warn("radio rejected packet", "res", qs.GetRes(), "id", qs.GetMeshPacketId(), "free", qs.GetFree())
		} else {
			c.log.Debug("radio queue", "free", qs.GetFree(), "max", qs.GetMaxlen())
		}
	case *meshpb.FromRadio_LogRecord:
		c.log.Debug("radio log", "source", v.LogRecord.GetSource(), "msg", v.LogRecord.GetMessage())
	}
	return false, nil
}

func (c *Client) requestConfig(conn io.Writer) error {
	nonce := randomNonce()
	c.mu.Lock()
	c.nonce = nonce
	c.status.Connected = false
	c.mu.Unlock()
	return c.send(conn, &meshpb.ToRadio{PayloadVariant: &meshpb.ToRadio_WantConfigId{WantConfigId: nonce}})
}

func (c *Client) send(conn io.Writer, msg *meshpb.ToRadio) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return WriteFrame(conn, b)
}

func (c *Client) writeRaw(conn io.Writer, b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := conn.Write(b)
	return err
}

func randomID() uint32 {
	for {
		if id := rand.Uint32(); id != 0 {
			return id
		}
	}
}

func randomNonce() uint32 {
	for {
		n := rand.Uint32()
		if n != 0 && n != specialNonceOnlyConfig && n != specialNonceOnlyNodes {
			return n
		}
	}
}
