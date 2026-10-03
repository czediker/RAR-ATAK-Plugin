# Deployment

## 1. Prepare each radio (once)

1. Flash the current openMANET image for the Pi 4 + WM6108 (SPI) and run its
   setup wizard.
2. Configure the RAK4631 Serial module for PROTO mode and wire the UART —
   see [hardware.md](hardware.md#rak4631--pi-uart).
3. Free the Pi UART (`enable_uart=1`, `dtoverlay=disable-bt`, no serial
   console) and optionally add the LED `gpio=` line — see
   [hardware.md](hardware.md#pi-4-put-the-full-uart-on-pins-810). Reboot.
4. Find the HaLow interface name: `batctl meshif bat0 if` lists the
   batman-adv hard interfaces (on openMANET the HaLow 802.11s interface is
   typically the one shown as `MainIF` in `batctl n`).

## 2. Build and install the Pi services

On a workstation with Go 1.25+:

```sh
cd pi
make test                          # unit tests
make install HOST=root@10.41.x.1   # cross-compile, copy, enable, start
```

`make install` streams a tarball over ssh (openMANET's dropbear has no SFTP),
installs `/usr/bin/rar-bridge`, `/usr/bin/rar-led`, the init scripts, and
`/etc/config/rar` (only if it does not exist yet; the shipped defaults are
also kept as `/etc/config/rar.default`).

Without `make`: `make dist` produces `build/rar-radio-<version>-arm64.tar.gz`;
extract it on the radio with `tar -xzf rar-radio-*.tar.gz -C /`, copy
`/etc/config/rar.default` to `/etc/config/rar`, then
`/etc/init.d/rar-led enable && /etc/init.d/rar-bridge enable` and start both.

## 3. Configure

All settings are in `/etc/config/rar` (UCI). The services restart
automatically on `uci commit rar`.

```sh
uci set rar.bridge.halow_iface='wlan0'   # replace the HALOW_IFACE stand-in
uci set rar.bridge.baud='115200'         # match the RAK Serial module
uci commit rar
```

| Option | Default | Meaning |
|--------|---------|---------|
| `bridge.serial_device` | `/dev/ttyAMA0` | RAK4631 UART |
| `bridge.baud` | `115200` | Must match the Meshtastic Serial module |
| `bridge.channel` | `0` | Meshtastic channel index for TAK traffic |
| `bridge.hop_limit` | `0` | 0 = the radio's own LoRa hop limit |
| `bridge.halow_iface` | `HALOW_IFACE` | HaLow hard interface (stand-in = any interface) |
| `bridge.poll` / `down_after` / `up_after` | `5s` / `15s` / `30s` | Neighbor check and hysteresis |
| `bridge.neighbor_max_age` | `5s` | Ignore neighbors not heard for this long |
| `bridge.eud_port` | `4242` | ATAK UDP input on the phone |
| `bridge.use_dhcp_leases` | `1` | Also deliver to this node's DHCP clients |
| `bridge.suppress_halow_dupes` | `1` | Drop Meshtastic copies of traffic already heard over HaLow |
| `bridge.mcast_iface` | `br-ahwlan` | Where to listen for ATAK multicast |
| `bridge.pli_interval` / `pli_min_interval` / `pli_move_meters` | `60s` / `15s` / `50` | Position rate limit |
| `bridge.tx_gap` | `3s` | Minimum gap between LoRa transmissions |
| `bridge.log_level` | `info` | `debug` shows every decision |
| `led.red` / `green` / `blue` | `16` / `20` / `21` | BCM GPIO numbers |
| `led.active_low` | `1` | 1 = direct common-anode drive, 0 = transistor drive |

## 4. Install the ATAK plugin

Build it inside your ATAK-CIV 5.8 SDK and install the APK — see
[atak-plugin/README.md](../atak-plugin/README.md).

In ATAK check *Settings → Network Preferences → Network Connection
Preferences → Manage Inputs* for a UDP input on port **4242**; traffic
received over Meshtastic is delivered there. If your ATAK build lacks it, add
`0.0.0.0:4242` (UDP) or change `rar.bridge.eud_port` to an input you have.

## 5. Verify

On the radio:

```sh
logread -f -e rar                 # live logs from both services
cat /var/run/rar/state.json       # current status
batctl meshif bat0 neighbors_json # what the HaLow monitor sees
```

Expected on a healthy radio near others: `radio ready node=!xxxxxxxx`,
`HaLow link state state=connected`, LED blue.

Bench tests:

| Test | Expect |
|------|--------|
| Walk out of HaLow range (or `ip link set <halow_iface> down`) | After ~15–20 s the LED turns green, log says "forwarding port 6700 traffic over Meshtastic"; the other radios show your position/chat with a `__rar` detail |
| Toggle **Always send over Meshtastic** in the plugin | LED turns cyan while HaLow is up; position goes out at most once a minute |
| Unplug the RAK4631 UART | LED alternates with red; queued chats wait (up to 10 min) |
| `/etc/init.d/rar-bridge stop` | LED solid red within 10 s |

## Troubleshooting

| Symptom | Check |
|---------|-------|
| `no config response from radio` in the log | Serial module not in PROTO mode, wrong baud, RX/TX swapped, or a console still on `ttyAMA0` |
| LED always green with neighbors present | `halow_iface` wrong; run `batctl meshif bat0 neighbors_json` and compare `hard_ifname` |
| Remote users never appear | Radios on different Meshtastic channels/keys; `log_level debug` shows every received packet |
| Received traffic not shown in ATAK | ATAK lacks a UDP 4242 input; plugin not loaded yet (the bridge learns the phone from plugin traffic or DHCP leases) |
| Plugin pane shows copies sent, but `euds` stays 0 in `state.json` (no `from plugin` lines at `log_level debug`) | The firewall drops UDP 6700/6701 from the mesh zone; see below |
| Chats appear twice | `suppress_halow_dupes` off, or `mcast_iface` wrong so the bridge cannot hear HaLow multicast |

### Firewall

openMANET puts the mesh, Ethernet and access point in the `ahwlan` firewall
zone. If that zone does not accept input, allow the plugin ports:

```sh
uci add firewall rule
uci set firewall.@rule[-1].name='Allow-RAR-plugin'
uci set firewall.@rule[-1].src='ahwlan'
uci set firewall.@rule[-1].proto='udp'
uci set firewall.@rule[-1].dest_port='6700-6701'
uci set firewall.@rule[-1].target='ACCEPT'
uci commit firewall && /etc/init.d/firewall restart
```
