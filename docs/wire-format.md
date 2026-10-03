# Wire formats

## Ports

| Port | Proto | From → To | Payload |
|------|-------|-----------|---------|
| 6700 | UDP | plugin → Pi | One CoT event (UTF-8 XML) per datagram. Forwarded to Meshtastic only while HaLow is isolated |
| 6701 | UDP | plugin → Pi | Same, always forwarded |
| 4242 | UDP | Pi → EUD | One CoT event per datagram, received over Meshtastic (ATAK's default UDP input) |
| 239.2.3.1:6969 | UDP multicast | ATAK ↔ mesh | ATAK SA; the Pi only listens (duplicate suppression) |
| 224.10.10.1:17012 | UDP multicast | ATAK ↔ mesh | ATAK GeoChat; the Pi only listens |

The plugin addresses the Pi by the DHCP server of the phone's Wi-Fi network.
The Pi addresses the EUD by the source address of 6700/6701 datagrams and
by its own DHCP leases.

## Meshtastic packets

* Port: `ATAK_PLUGIN` (72), payload = `meshtastic.TAKPacket` protobuf,
  `is_compressed = false`, at most 233 bytes.
* Broadcast (`to = 0xffffffff`), channel index from config (default 0),
  `hop_limit` = configured value or the radio's own LoRa hop limit,
  `want_ack = false`.

The serial link uses the standard Meshtastic stream API framing:
`0x94 0xC3 <len hi> <len lo> <ToRadio/FromRadio protobuf>`. The bridge sends
`want_config_id` on connect, waits for `config_complete_id`, sends a
`heartbeat` every 60 s, and reconnects/reconfigures on errors or `rebooted`.

### CoT → TAKPacket

| CoT | TAKPacket |
|-----|-----------|
| `event@uid` (PLI) / chat sender UID | `contact.device_callsign` |
| `detail/contact@callsign` / `__chat@senderCallsign` | `contact.callsign` |
| `detail/__group@name`, `@role` | `group.team`, `group.role` (enum; chats use the sender's last PLI) |
| `detail/status@battery` | `status.battery` |
| `point@lat`, `@lon` | `pli.latitude_i`, `pli.longitude_i` (× 1e7) |
| `point@hae` (9999999 = unknown) | `pli.altitude` (m, 0 when unknown) |
| `detail/track@speed` (m/s), `@course` (°) | `pli.speed`, `pli.course` (rounded) |
| `remarks` text | `chat.message` (≤ 200 bytes, truncated to fit 233) |
| `chatgrp@uid1` (`All Chat Rooms` or recipient UID) | `chat.to` |
| `__chat@chatroom` for direct messages | `chat.to_callsign` |

### TAKPacket → CoT

* PLI: `type="a-f-G-U-C" how="m-g"`, stale 6 min, `hae` 9999999 when
  altitude is 0, `contact`, `uid@Droid`, `__group`, `status`, `track`.
* Chat: GeoChat `b-t-f` in ATAK's own layout (`__chat`, `chatgrp`, `link`,
  `remarks`), UID `GeoChat.<sender>.<room>.<messageId>`, stale 24 h,
  placed at the sender's last known position. `messageId` is a UUID
  derived from (sender node, packet ID, sender UID, text).
* Both carry `<__rar via="meshtastic" node="!xxxxxxxx"/>` in `detail`.

Receipts (`receipt_type != None`) and generic `detail` payloads are ignored.

Newer Meshtastic releases also define `TAKPacketV2` on `ATAK_PLUGIN_V2` (78)
with zstd dictionary compression. The bridge uses the original `TAKPacket`
on port 72, which current firmware passes through unchanged.

## Status file

`/var/run/rar/state.json`, replaced atomically:

```json
{
  "version": 1,
  "updated": "2026-10-03T20:08:06.718Z",
  "halow": { "state": "connected|isolated|unknown", "neighbors": 1, "error": "" },
  "meshtastic": { "connected": true, "node": "!a1b2c3d4", "error": "" },
  "forwarding": false,
  "forced": true,
  "tx_count": 12,
  "rx_count": 3,
  "last_tx": "2026-10-03T20:07:59Z",
  "last_rx": "2026-10-03T20:05:10Z",
  "euds": 1,
  "queued_chats": 0,
  "queued_plis": 0
}
```

* `forwarding` — port 6700 traffic is going to Meshtastic.
* `forced` — port 6701 traffic seen in the last 90 s.
* `tx_count` / `rx_count` — TAKPackets sent / accepted; the LED flashes when
  they change.
