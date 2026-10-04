# Deployment

## 1. Prepare each radio (once)

1. Flash the current openMANET image for the Pi 4 + WM6108 (SPI) and run its
   setup wizard.
2. Configure the RAK4631's Serial module for PROTO mode with GPS mode
   `NOT_PRESENT`, and connect it to **uart2 on Pi pins 27/28**: the WM1302
   HAT's own GPS occupies the primary UART (pins 8/10). This means checking
   that the pins are free, adding `dtoverlay=uart2` to `config.txt`, and a
   loopback test. See
   [hardware.md](hardware.md#connecting-the-rak4631-uart2-pins-2728).
3. Optionally add the LED `gpio=` line to `config.txt`; see
   [hardware.md](hardware.md#keep-the-led-off-during-boot). Reboot.
4. Find the HaLow interface name: `batctl meshif bat0 if` lists the
   batman-adv hard interfaces (on openMANET the HaLow 802.11s interface is
   typically the one shown as `MainIF` in `batctl n`).

## 2. Build and install the Pi services

You need Go 1.25+ on your workstation. One command builds the three
programs for the Pi 4, copies them to the radio and runs the installer
there (`rar-install.sh`), which stops the services, installs the files,
updates the settings and starts everything again. Install and upgrade are
the same command.

**Windows (PowerShell):**

```powershell
cd pi
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Radio 10.41.x.1
```

**Linux / macOS / WSL:**

```sh
cd pi
make install HOST=root@10.41.x.1
```

You are asked for the radio's root password (twice on Windows) unless you
use an SSH key.

### Settings file on upgrade

`/etc/config/rar` is backed up to `/etc/config/rar.bak` on every install.

* Plain install/upgrade keeps your settings and only adds sections that are
  new in the release (e.g. `global.debug`).
* **Reset to the new defaults** with `-ResetConfig` (Windows) or `RESET=1`
  (make). This radio's own settings are carried over: `halow_iface`,
  `mesh_iface`, `serial_device`, `baud`, `channel`, `hop_limit`,
  `mcast_iface` and `global.debug`. Everything else, including the LED
  polarity, takes the release default.

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Radio 10.41.x.1 -ResetConfig
```

```sh
make install HOST=root@10.41.x.1 RESET=1
```

The installer prints the resulting settings. `rar-bridge` and `rar-led` share
the status file format, so always install both from the same build (both
commands do).

By hand: `make dist` produces `build/rar-radio-<version>-arm64.tar.gz`. Copy
it to the radio, unpack it into an empty directory and run
`sh rar-install.sh [--reset-config]` from there.

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
| `bridge.serial_device` | `/dev/ttyAMA2` | Serial device of the RAK4631: uart2 on pins 27/28 (check with `dmesg \| grep fe201400`) |
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
| `global.debug` | `0` | 1 = detailed debug logging from both services (see below) |
| `bridge.log_level` | `info` | Log level when `global.debug` is 0 |
| `led.red` / `green` / `blue` | `16` / `20` / `21` | BCM GPIO numbers (red reserved) |
| `led.active_low` | `0` | 0 = common-cathode LED (pin high = on), 1 = common anode |
| `led.red_file` | unset | Hook for the reserved red LED (`on` / `off` / `blink` in the file) |

## 4. Install the ATAK plugin

Build it inside your ATAK-CIV 5.8 SDK and install the APK — see
[atak-plugin/README.md](../atak-plugin/README.md).

In ATAK check *Settings → Network Preferences → Network Connection
Preferences → Manage Inputs* for a UDP input on port **4242**; traffic
received over Meshtastic is delivered there. If your ATAK build lacks it, add
`0.0.0.0:4242` (UDP) or change `rar.bridge.eud_port` to an input you have.

## 5. Verify

First check the UART link to the RAK4631 with `rar-meshtest` — it sends
the text messages `1` … `10` on the primary channel (see
[hardware.md](hardware.md#uart-link-test-rar-meshtest)):

```sh
/etc/init.d/rar-bridge stop && rar-meshtest; /etc/init.d/rar-bridge start
```

Then, on the radio:

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
| Walk out of HaLow range (or `ip link set <halow_iface> down`) | After ~15–20 s blue goes off and green turns solid; log says "FALLOVER"; the other radios show your position/chat with a `__rar` detail |
| Toggle **Always send over Meshtastic** in the plugin | LED stays blue while HaLow is up; with debug on, the log shows each position passed to the radio (at most once a minute) |
| Unplug the RAK4631 UART | Green blinks; queued chats wait (up to 10 min) |
| Make `batctl` fail (e.g. a wrong `mesh_iface`) | Blue blinks; green turns solid after 15 s (no neighbors can be confirmed) |
| `/etc/init.d/rar-bridge stop` | Blue and green blink together within 10 s |

## Debug logging

Turn on detailed logging for both services (they restart automatically):

```sh
uci set rar.global.debug=1; uci commit rar
logread -f -e rar            # follow both services live
```

Turn it off again with `uci set rar.global.debug=0; uci commit rar`. To watch
on the console instead of syslog, stop a service and run it in the
foreground, e.g. `/etc/init.d/rar-bridge stop; rar-bridge -debug -halow-iface wlan0`.

Every line names the service (`service=rar-bridge` / `service=rar-led`).
What a fallover with traffic in both directions looks like (timestamps
trimmed):

```
service=rar-bridge … msg="plugin message received" port=auto src=10.41.113.200 cot_bytes=343 forward=false kind=position uid=ANDROID-self callsign=ALPHA lat=38.8977 lon=-77.0365 hae=25.3 team=Cyan role="Team Lead" battery=88 …
service=rar-bridge … msg="not sent to Meshtastic: HaLow has neighbors and this arrived on the auto port"
service=rar-bridge … msg="no HaLow neighbors; switching to Meshtastic fallback unless they return" in=15s
service=rar-bridge … msg="HaLow link state committed" state=isolated neighbors=0
service=rar-bridge … msg="no HaLow neighbors; FALLOVER: forwarding port 6700 traffic over Meshtastic"
service=rar-bridge … msg="state.json change committed" changes="forwarding: false → true; halow.state: connected → isolated"
service=rar-led    … msg="LED change committed" color=blue from=solid to=off reason="HaLow isolated"
service=rar-led    … msg="LED change committed" color=green from=off to=solid reason="fallover: traffic going over Meshtastic"
service=rar-bridge … msg="converted to TAKPacket" takpacket_bytes=51 max=233 uid=ANDROID-self callsign=ALPHA …
service=rar-bridge … msg="passed to Meshtastic radio" kind=position who=ALPHA uid=ANDROID-self bytes=51 packet_id=1650537516
service=rar-bridge … msg="Meshtastic radio accepted the message for transmission" packet_id=1650537516 queue_free=15 queue_max=16
service=rar-bridge … msg="ATAK data received from Meshtastic" from=!1111beef packet_id=4242 rssi=-97 snr=6.5 hop_limit=2 hop_start=3 payload_bytes=60 uid=ANDROID-bravo callsign=BRAVO kind=chat to="All Chat Rooms" text_bytes=16
service=rar-bridge … msg="sent to ATAK" kind=chat uid=ANDROID-bravo dest=10.41.113.200:4242 cot_bytes=869
service=rar-bridge … msg="DUPLICATE Meshtastic packet; already handled" from=!1111beef packet_id=4242 first_seen_ago=301ms
```

What is logged:

| Event | Messages |
|-------|----------|
| Plugin traffic on 6700/6701 | `UDP datagram from plugin`, `plugin message received` (size, ATAK UID, callsign, type, position, team, role, battery, speed/course, or chat recipient and text size — never the text itself), `not sent to Meshtastic: …`, `converted to TAKPacket` (size), `position queued` / `chat queued` (incl. rate limit wait), `newer position replaces an unsent queued one` |
| Hand-off to Meshtastic | `passed to Meshtastic radio` / `FAILED to pass message to Meshtastic radio`, then `Meshtastic radio accepted …` / `REJECTED …` / `did not confirm …` |
| Received from Meshtastic | `ATAK data received from Meshtastic` (node, packet ID, RSSI, SNR, hops, size, ATAK UID/metadata), `sent to ATAK` (ip:port) / `FAILED to send to ATAK`, non-ATAK packets (`not ATAK traffic; ignored`, with port and size) |
| De-duplication | `DUPLICATE Meshtastic packet`, `DEDUPE: sender is reachable over HaLow …`, `TAKPacket is from this radio's own EUD`, `ATAK user heard over HaLow …`, `direct message for another user` |
| State | `state.json change committed` (field-by-field diff), `HaLow link state committed`, `HaLow neighbor set changed`, fallover countdown / cancel, `openMANET neighbor query failed`, multicast listener errors |
| Radio | serial port opened, config handshake, firmware/preset/channels, heartbeat, radio ready / not available, radio console and log lines |
| EUDs | `local EUD learned`, `EUD delivery targets changed`, DHCP client list |
| LED | `LED change committed` (color, from, to, reason), `LED reason changed`, status file unreadable / readable again |

UDP has no delivery receipt, so `sent to ATAK` means the datagram left the
Pi; whether ATAK showed it is visible on the phone.

## Troubleshooting

| Symptom | Check |
|---------|-------|
| `serial port problem: process … (gpsd) has /dev/ttyAMA0 open` | The WM1302 HAT's GPS is wired to the primary UART (pins 8/10), and openMANET's gpsd reads it. Move the RAK4631 to uart2 on pins 27/28 — see [Connecting the RAK4631](hardware.md#connecting-the-rak4631-uart2-pins-2728) |
| `no config response from radio` in the log | Read `received_bytes` and `hint` in the same line. `received_bytes=0` means nothing comes back from the radio: check nothing else is wired to the Pi's RX pin (the WM1302 HAT's GPS is on pin 10), set the RAK's GPS mode to `NOT_PRESENT`, and check the RAK TXD1 → Pi RX wire (pin 28 for uart2). Otherwise: Serial module not in PROTO mode, wrong baud, RX/TX swapped, or a console still on `ttyAMA0`. See [the UART link test](hardware.md#uart-link-test-rar-meshtest) |
| `serial port was hung up` (older versions: `read: Port has been closed`), repeating | Another program or a login console is using the radio's port; the `serial port problem` lines name it. A login console on the UART: remove the serial console and confirm with `cat /proc/cmdline` after a reboot, see [the console section](hardware.md#pi-4-put-the-full-uart-on-pins-810). gpsd: see the row above |
| `serial port problem: …` | A Linux console or login is on the radio's UART, or another program has it open (named in the line). Logged at startup and while the radio is disconnected |
| A phone connected to the radio over Bluetooth drops when the bridge connects | Expected with Meshtastic firmware 2.7.x and older: the radio turns Bluetooth off while the bridge is connected. It also shows that the Pi → radio direction works. Stop `rar-bridge` to use the phone app — see [Bluetooth while the bridge is connected](hardware.md#bluetooth-while-the-bridge-is-connected) |
| LED always green with neighbors present | `halow_iface` wrong; run `batctl meshif bat0 neighbors_json` and compare `hard_ifname` |
| Remote users never appear | Radios on different Meshtastic channels/keys; with `global.debug=1` every received packet is logged (undecryptable ones too) |
| Received traffic not shown in ATAK | ATAK lacks a UDP 4242 input; plugin not loaded yet (the bridge learns the phone from plugin traffic or DHCP leases) |
| Plugin pane shows copies sent, but `euds` stays 0 in `state.json` (no `UDP datagram from plugin` lines with `global.debug=1`) | The firewall drops UDP 6700/6701 from the mesh zone; see below |
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
