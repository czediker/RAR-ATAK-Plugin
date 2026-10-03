# RAR dual-band radio

Software for a handheld ATAK radio with two meshes:

* **Primary — Wi-Fi HaLow** (Seeed Wio-WM6108 on a Raspberry Pi 4 running
  openMANET): short range, high data rate. ATAK uses it normally.
* **Fallback — Meshtastic LoRa** (RAK4631 on the Pi's UART): long range, low
  data rate. Used for ATAK chat and position reports when the radio has no
  HaLow neighbors, or always when the user asks for it.

```
 ┌──────────── EUD (Android, ATAK-CIV 5.8) ────────────┐
 │ ATAK ── SA/chat over Wi-Fi ──────────────────────┐  │
 │ RAR Dual-Band plugin ── copies own PLI + chat ─┐ │  │
 └────────────────────────────────────────────────│─│──┘
                  UDP 6700 (auto) / 6701 (always) │ │ multicast 6969 / 17012
 ┌──────────── Raspberry Pi 4 (openMANET) ────────▼─▼──────────────────────┐
 │ rar-bridge                                       br-ahwlan ── bat0 ── HaLow mesh
 │  ├─ HaLow monitor: batctl neighbors every 5 s (15 s down / 30 s up)     │
 │  ├─ 6700 → Meshtastic only while isolated;  6701 → always               │
 │  ├─ CoT ⇄ TAKPacket (ATAK_PLUGIN port 72), LoRa rate limiting           │
 │  ├─ Meshtastic RX → CoT → UDP 4242 on the local EUD                     │
 │  └─ writes /var/run/rar/state.json                                      │
 │ rar-led ── reads state.json ── RGB LED (GPIO 16/20/21)                  │
 └──────────────────────────────── /dev/ttyAMA0 ───────────────────────────┘
                                        │ UART (Meshtastic Serial module, PROTO mode)
                                 RAK4631 (Meshtastic) ── LoRa mesh
```

## Repository layout

| Path | Contents |
|------|----------|
| [`pi/`](pi) | Go module for the two Pi services: `rar-bridge` and `rar-led`, OpenWrt init scripts and UCI config |
| [`atak-plugin/`](atak-plugin) | ATAK-CIV 5.8 plugin (Java, SDK plugintemplate layout) |
| [`docs/architecture.md`](docs/architecture.md) | How the pieces behave: forwarding policy, timing, de-duplication, extension points |
| [`docs/wire-format.md`](docs/wire-format.md) | Ports, CoT ⇄ TAKPacket mapping, status file schema |
| [`docs/hardware.md`](docs/hardware.md) | UART and LED wiring, `config.txt`, Meshtastic serial settings |
| [`docs/deployment.md`](docs/deployment.md) | Build, install, configure and verify on a radio |

## LED

| LED | Meaning |
|-----|---------|
| Blue | HaLow has neighbors; ATAK traffic stays on HaLow |
| Green | No HaLow neighbors; chat and position go out over Meshtastic |
| Cyan | HaLow has neighbors and the plugin's **Always send over Meshtastic** is on |
| Color alternating with red | Meshtastic radio not connected (check UART / Serial module) |
| Short white flash | A packet was sent or received over Meshtastic |
| Slow white blink | Starting up, waiting for `rar-bridge` |
| Solid red | `rar-bridge` is not running |

## Quick start

```sh
# Pi services: test, cross-compile for the Pi 4 and install on a radio
cd pi
make test
make install HOST=root@<radio-ip>
ssh root@<radio-ip> "uci set rar.bridge.halow_iface='wlan0'; uci commit rar"   # your HaLow interface
```

Build the plugin inside your ATAK SDK — see [`atak-plugin/README.md`](atak-plugin/README.md).
Full steps, including the Pi UART setup the RAK4631 needs, are in
[`docs/deployment.md`](docs/deployment.md).

## Current scope

* An isolated radio **sends** over Meshtastic but receives only what other
  radios send over Meshtastic (their "always" traffic, or other isolated
  radios). Radios that still have HaLow neighbors do not relay HaLow traffic
  to it yet. The bridge already tracks which users are reachable over HaLow
  (`internal/halowseen`), which is the hook for adding that relay later —
  see [architecture](docs/architecture.md#extension-point-relaying-to-isolated-radios).
* Only position reports and GeoChat are carried. Other CoT (markers, routes,
  alerts) stays on HaLow.

## Licensing note

`pi/proto/meshtastic` and the generated `pi/internal/meshpb` come from
[meshtastic/protobufs](https://github.com/meshtastic/protobufs), which is
GPL-3.0. Distributing the Pi binaries carries GPL-3.0 obligations.
