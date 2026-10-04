# Architecture

Three programs cooperate. None of them needs a configured IP address.

| Program | Runs on | Job |
|---------|---------|-----|
| RAR Dual-Band plugin | EUD (ATAK-CIV 5.8) | Copies the device's own position reports (PLI) and sent GeoChat messages to the radio on UDP 6700, or 6701 when "always send over Meshtastic" is on |
| `rar-bridge` | Raspberry Pi | Decides what goes over Meshtastic, talks to the RAK4631, delivers received Meshtastic traffic to the EUD, publishes status |
| `rar-led` | Raspberry Pi | Shows the status on the RGB LED |

## openMANET network facts the design relies on

* `bat0` (batman-adv over the HaLow 802.11s interface), Ethernet and the
  2.4 GHz access point are bridged into `br-ahwlan`: the whole mesh is **one
  flat broadcast domain** (10.41.0.0/16). Anything the Pi multicasts or
  broadcasts reaches every radio.
* Every node runs its **own DHCP server**, so the phone's DHCP server is the
  radio it is connected to. The plugin uses that to find its radio; the Pi
  uses its own lease file (`/tmp/dhcp.leases`) to find its phone.

## Outbound: EUD → Meshtastic

```
plugin ──UDP 6700/6701──▶ rar-bridge ──CoT→TAKPacket──▶ queue ──▶ RAK4631 ──▶ LoRa
```

1. The plugin hooks ATAK's send path and copies own PLI (`a-*` with the
   device UID) and own chat (`b-t-f` with a `GeoChat.<uid>.` UID) as raw
   CoT XML, one event per UDP datagram. ATAK's normal HaLow traffic is
   untouched.
2. `rar-bridge` learns the sender's address and ATAK UID (used later to
   deliver received traffic and to recognise its own user).
3. Forwarding decision:
   * port **6701** → always forwarded;
   * port **6700** → forwarded only while the HaLow link is **isolated** (or
     not yet determined right after boot — failing towards Meshtastic means
     a radio that boots out of range is never silent).
4. The CoT event is converted to a `TAKPacket` (see
   [wire-format](wire-format.md)), at most 233 bytes; long chat text is
   truncated.
5. A single transmit queue enforces airtime limits:
   * chats first, in order (max 32 queued, dropped after 10 min unsent);
   * position reports: only the latest per user is kept, sent at most once
     per **60 s**, or after **15 s** if the user moved **≥ 50 m**;
   * at least **3 s** between any two transmissions;
   * queued 6700 position reports are discarded when HaLow comes back.
6. Packets go out as broadcasts on the configured channel (default 0) with
   the radio's configured hop limit.

## HaLow link state

`rar-bridge` runs `batctl meshif bat0 neighbors_json` every 5 s (falling
back to the text `neighbors` output on old batctl) and counts neighbors on
the HaLow interface heard within the last 5 s.

* Connected → isolated after **15 s** continuously without neighbors.
* Isolated → connected after **30 s** continuously with neighbors.
* A failed `batctl` call counts as "no neighbors".

The decision is per radio: two radios that wander off together still see
each other and stay on HaLow, as specified.

The HaLow interface is a setting (`halow_iface`). It ships as the stand-in
`HALOW_IFACE`; while it is left that way, neighbors on any batman-adv
interface count (and a warning is logged).

## Inbound: Meshtastic → EUD

```
LoRa ──▶ RAK4631 ──▶ rar-bridge ──TAKPacket→CoT──▶ UDP 4242 on the local EUD(s)
```

Every `ATAK_PLUGIN` packet the RAK4631 receives is considered, then:

1. Duplicates (same sender node + packet ID within 10 min) are dropped.
2. Compressed TAKPackets (unishox2, older official plugin/firmware) are
   skipped; those firmwares also hand the client an uncompressed copy with
   the same ID, which is used instead.
3. Traffic from the local user's own UID is dropped.
4. **HaLow duplicate suppression:** `rar-bridge` passively listens to ATAK's
   SA (239.2.3.1:6969) and GeoChat (224.10.10.1:17012) multicast on
   `br-ahwlan`. If the sender was heard over HaLow in the last 60 s, the EUD
   already has the message, so the Meshtastic copy is dropped. This matters
   when a sender uses "always send over Meshtastic": without it every chat
   would appear twice.
5. Direct messages addressed to someone else are dropped (when the local
   user's UID is known).
6. The packet is rebuilt as CoT (PLI `a-f-G-U-C`, or GeoChat placed at the
   sender's last known position) with a `<__rar via="meshtastic" .../>`
   marker and **unicast** to the local EUD(s) on UDP 4242 — ATAK's default
   UDP input. Delivery targets are the addresses the plugin has sent from
   (last 15 min) plus this node's DHCP clients.

Delivery is local-only by design: nothing received over Meshtastic is put
back on the HaLow mesh, so N radios hearing the same LoRa packet never
create N copies on HaLow. Each radio relies on its own RAK4631 hearing the
packet, which is normal because LoRa reaches further than HaLow.

GeoChat message IDs are derived from the Meshtastic packet identity, so the
same packet always maps to the same ATAK message and ATAK de-duplicates it.

## Status and LED

`rar-bridge` writes `/var/run/rar/state.json` (tmpfs) every second and
immediately on changes. `rar-led` reads it once a second and runs a separate
500 ms clock only to toggle blinking colors; GPIO lines are written only
when the color changes (measured: under 0.1 % of one core). The services
share nothing else, so either can restart independently; a status file older
than 10 s means the bridge is down. See
[wire-format](wire-format.md#status-file) for the schema and the
[README](../README.md#led) for the colors.

The bridge sets two fault flags for the LED:

* `halow.fault` — the `batctl` neighbor query failed on the last poll, or an
  ATAK multicast listener on `br-ahwlan` is down → blue blinks.
* `meshtastic.fault` — the radio is not connected (no config handshake), or
  within the last 30 s a packet could not be handed to it, was rejected
  (QueueStatus error) or was not confirmed within 10 s → green blinks.

## Debug logging

With `rar.global.debug=1` both services log at debug level, every line
tagged `service=rar-bridge` or `service=rar-led` (see
[deployment](deployment.md#debug-logging) for an annotated example). The
bridge logs each plugin datagram with its ATAK IDs and metadata, the
forwarding decision, the TAKPacket size, the hand-off to the radio and the
radio's confirmation, each received Meshtastic packet (RSSI/SNR/hops) with
its ATAK content, every delivery to ATAK (ip:port), every de-duplication,
every committed state.json change (as a field diff), HaLow neighbor changes
and fallover countdowns, radio handshake details, and EUD/DHCP target
changes. The LED service logs every committed pattern change with its
reason, and status file problems.

## Extension point: relaying to isolated radios

Today an isolated radio only receives what other radios choose to send over
Meshtastic. The pieces needed to relay HaLow traffic to it are already in
place:

* `halowseen.Tracker` knows which ATAK UIDs are currently reachable over
  HaLow (`UIDs()`).
* `rar-bridge` sees every UID heard over Meshtastic in `HandleRadio`.

A UID heard over Meshtastic but absent from the HaLow tracker is an isolated
radio. A future rule in `bridge.HandleEUD` can then forward port-6700
traffic while such a peer exists (each radio forwarding only its own
user's traffic avoids loops), without changing the plugin, the wire format
or the LED service.

## Code map (`pi/`)

| Package | Responsibility |
|---------|----------------|
| `cmd/rar-bridge` | Flags, wiring of goroutines |
| `cmd/rar-led` | LED loop |
| `internal/bridge` | Forwarding policy, queues, rate limiting, event loop, UDP I/O |
| `internal/halow` | batctl neighbor parsing, hysteresis, monitor loop |
| `internal/meshtastic` | Stream API framing and client (handshake, heartbeat, reconnect, link diagnosis, disconnect on shutdown) |
| `internal/uartcheck` | Finds a Linux console or another program on the radio's serial port |
| `internal/takconv` | CoT ⇄ TAKPacket conversion |
| `internal/cot` | CoT XML parsing/building |
| `internal/halowseen` | ATAK multicast listener / reachable-over-HaLow tracker |
| `internal/eud` | Local EUD registry and DHCP lease reader |
| `internal/state` | Status file shared with `rar-led` |
| `internal/led` | Status → color logic and GPIO driver |
| `internal/meshpb` | Generated Meshtastic protobufs (`make proto`) |
