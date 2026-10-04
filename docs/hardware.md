# Hardware setup

## Pin usage on the Pi 4 header

| Physical pin | BCM GPIO | Used by |
|--------------|----------|---------|
| 19, 21, 23, 24 | 10, 9, 11, 8 | HaLow HAT (SPI0) |
| 11, 29 | 17, 5 | WM1302 HAT reset lines (check your HAT revision for others) |
| **8** | **14 (TXD)** | → RAK4631 RXD1 |
| **10** | **15 (RXD)** | ← RAK4631 TXD1 |
| **36** | **16** | LED red anode (reserved, held off) |
| **38** | **20** | LED green anode (Meshtastic) |
| **40** | **21** | LED blue anode (HaLow) |
| 39 (or any ground) | — | LED common cathode |
| 6, 9, 14, 20, 25, 30, 34, 39 | — | Ground (one to the RAK4631) |

GPIO 16/20/21 avoid the HaLow HAT's SPI/reset/IRQ pins, the UART and I²C.
They can be changed with `uci set rar.led.red=...` etc.

## RAK4631 ↔ Pi UART

Wiring (both sides are 3.3 V logic):

* Pi pin 8 (GPIO14 TXD) → RAK4631 **RXD1** (nRF52 P0.15)
* Pi pin 10 (GPIO15 RXD) ← RAK4631 **TXD1** (nRF52 P0.16)
* Ground ↔ ground

### Meshtastic settings on the RAK4631

The bridge talks to the radio with the Meshtastic protobuf API, which the
RAK exposes on its UART only when the **Serial module runs in PROTO mode**:

| Setting | Value |
|---------|-------|
| Serial module → enabled | true |
| mode | **PROTO** |
| rxd / txd | 15 / 16 (RXD1 / TXD1) |
| baud | 115200 (must match `rar.bridge.baud`) |
| Position → GPS mode | **NOT_PRESENT** |

With the Meshtastic CLI over USB, for example:

```sh
meshtastic --set serial.enabled true --set serial.mode PROTO \
           --set serial.rxd 15 --set serial.txd 16 --set serial.baud BAUD_115200 \
           --set position.gps_mode NOT_PRESENT
```

**GPS mode must be `NOT_PRESENT`; `DISABLED` is not enough.** The RAK4631
firmware's GPS driver uses the same pins, 15 and 16. Unless the GPS mode is
`NOT_PRESENT`, the radio opens that GPS serial port at boot alongside the
Serial module. The radio still receives the Pi's messages, but its replies
don't reach the Pi, so the bridge logs `no config response from radio` with
`received_bytes=0`. Meshtastic's own
[serial module docs](https://meshtastic.org/docs/configuration/module/serial/)
give the same instruction. Don't fit a WisBlock GNSS module either: it would
be wired to the same pins. ATAK takes its position from the phone, so the
radio doesn't need a GPS.

(Some RAK19003 base boards bring out UART0 instead: use rxd 19 / txd 20 and
wire the Pi to RXD0/TXD0.)

### Bluetooth while the bridge is connected

When `rar-bridge` (or `rar-meshtest`) connects over the UART, the radio
treats it as an app connected over serial:

* **Firmware 2.7.x and older turn Bluetooth off while a serial app is
  connected.** A phone connected over Bluetooth is dropped as soon as the
  bridge connects, and can't reconnect while the bridge runs. If the phone
  drops when the UART is plugged in, the radio is hearing the Pi. When the
  bridge stops, it tells the radio it is leaving, and Bluetooth comes back
  straight away. Without that message, for example after a power cut or with
  the wires pulled, Bluetooth comes back 15 minutes after the link goes
  quiet. To change settings from the phone app, run
  `/etc/init.d/rar-bridge stop`, make the changes, then start the bridge
  again.
* **Firmware 2.8.0 and newer keep Bluetooth on (nRF52).** The phone app and
  the bridge then share one queue of received packets and send
  confirmations, and each item goes to only one of them. While a phone is
  connected, the bridge misses some ATAK traffic, and missing confirmations
  blink the green LED. Use Bluetooth for setup, then disconnect the app. You
  can also turn Bluetooth off for field use with
  `meshtastic --set bluetooth.enabled false`.

All radios should run the same Meshtastic firmware release, share the
channel used for TAK traffic, and use the default LoRa hop limit or set
`rar.bridge.hop_limit`.

### Pi 4: put the full UART on pins 8/10

On a Pi 4, `/dev/ttyAMA0` (PL011) drives Bluetooth by default. In the boot
partition's `config.txt` (on OpenWrt usually `/boot/config.txt`):

```ini
enable_uart=1
dtoverlay=disable-bt
```

This does not affect the onboard Wi-Fi access point.

The UART must not also be the Linux console, or kernel messages and a login
prompt will be sent to the RAK4631:

* delete the `console=serial0,115200` (or `console=ttyAMA0,...`) entry from
  `cmdline.txt` in the boot partition. Delete only that entry; do not
  replace it with another serial console. Leave the rest of the line as
  it is, especially `root=...` and `rootwait`, and keep the file on a
  single line. OpenWrt 24.10's stock file changes from

  ```
  console=tty1 console=serial0,115200 root=PARTUUID=xxxxxxxx-02 rootfstype=squashfs,ext4 rootwait
  ```

  to

  ```
  console=tty1 root=PARTUUID=xxxxxxxx-02 rootfstype=squashfs,ext4 rootwait
  ```

  `console=tty1` (the HDMI screen) stays. If the serial entry was the only
  `console=` on your line, put `console=tty1` in its place;
* make sure `/etc/inittab` has no login on `ttyAMA0` (OpenWrt's
  `::askconsole:` line follows the kernel console, so it moves off the UART
  once the console is removed).

Check after a reboot: `dmesg | grep ttyAMA0` should show the PL011 UART,
and `logread -e rar-bridge` should report `radio ready` with the RAK's node
ID.

### UART link test (`rar-meshtest`)

`rar-meshtest` (installed with the services) broadcasts ten text messages,
`1` … `10`, on the radio's primary channel and then exits. They show up in
that channel's chat on every other radio in range. It checks both
directions of the UART: the radio must answer the config handshake and
acknowledge each message it is handed.

```sh
/etc/init.d/rar-bridge stop     # only one program can use the UART
rar-meshtest                    # options: -serial, -baud, -channel, -count, -interval
/etc/init.d/rar-bridge start
```

Expected output:

```
Opening /dev/ttyAMA0 at 115200 baud...
Radio answered: node !a1b2c3d4, firmware 2.7.x, hop limit 3, preset LONG_FAST
Sending 10 text messages on channel 0 (default name, PRIMARY), 5s apart

[ 1/10] "1" accepted by the radio (id 53deea4b, TX queue 15/16 free)
...
[10/10] "10" accepted by the radio (id 4d40c0b1, TX queue 15/16 free)

Waiting 5s for the radio to finish transmitting...
PASS: all 10 messages were accepted by the radio. Check that they appear on the other radios.
```

Exit code 0 = all accepted, 1 = some not accepted, 2 = no answer from the
radio. "Accepted" means the RAK received the message over the UART and
queued it for LoRa; seeing `1`…`10` on another radio confirms the RF side.

When the radio doesn't answer, `rar-meshtest` shows how much came back from
it and what that points to. It also lists anything on the Pi that gets in
the way: a Linux console on the port, or another program with it open.

```
FAIL: no answer from the radio within 30s.
  Last error: no config response from radio (0 bytes, 0 API frames received in 20s)
  Received from the radio: 0 bytes, 0 API frames
  -> nothing arrived from the radio: check the RAK TXD1 -> Pi pin 10 wire, ...
```

| Received from the radio | Meaning |
|-------------------------|---------|
| 0 bytes | Nothing arrives from the radio. If a phone was dropped from Bluetooth at the same moment (firmware 2.7.x and older), the radio hears the Pi, so only the radio → Pi direction is broken: GPS mode not `NOT_PRESENT`, or the RAK TXD1 → Pi pin 10 wire. Otherwise also check the Serial module settings and the Pi → RAK wire |
| bytes, 0 API frames | The baud rate doesn't match, the Serial module isn't in PROTO mode, or the radio's GPS driver is also using the pins |
| API frames, but no handshake | Data is being lost: another program on the Pi is reading the port (a login console, gpsd) |

To test the Pi's side on its own, unplug the radio, connect Pi pin 8
directly to pin 10 with a jumper wire, and run:

```sh
/etc/init.d/rar-bridge stop
rar-meshtest -loopback          # PASS = the Pi's UART works; any fault is on the radio side
```

## RGB LED (common cathode)

```
GPIO21 (40) ── R_blue ──▶|── blue  ┐
GPIO20 (38) ── R_green ─▶|── green ├── common cathode ── GND (pin 39)
GPIO16 (36) ── R_red ───▶|── red   ┘   (red reserved)
```

* Default `active_low=0`: **pin high (3.3 V) = color on**.
* Resistors: size from the LED datasheet for ≤ 8 mA per pin. Typical values
  are ~150 Ω for red and 22–47 Ω for green/blue. At 3.3 V green and blue are
  noticeably dimmer than red because their forward voltage is ~3 V. For a
  brighter LED, drive each anode from 5 V through a high-side switch (for
  example a PNP transistor or P-channel MOSFET with its own small driver),
  keeping `active_low` matched to the driver's logic.
* Red is wired but reserved for a future low-battery indication. `rar-led`
  claims the pin and holds it low. To drive it later without code changes,
  set `uci set rar.led.red_file=/var/run/rar/led-red` and write `on`, `off`
  or `blink` to that file.
* If a common-anode LED is ever used instead, set `uci set rar.led.active_low=1`.

### Keep the LED off during boot

Until `rar-led` starts, the pins are inputs. Drive them low (off) from
boot by adding to `config.txt`:

```ini
gpio=16,20,21=op,dl
```
