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

With the Meshtastic CLI over USB, for example:

```sh
meshtastic --set serial.enabled true --set serial.mode PROTO \
           --set serial.rxd 15 --set serial.txd 16 --set serial.baud BAUD_115200
```

RAK4631 firmware also maps a WisBlock GNSS module to these pins; leave GPS
off (or use a module in another slot) when the UART is used for the bridge.

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
radio (it then prints a checklist: PROTO mode, baud, crossed TX/RX, serial
console). "Accepted" means the RAK received the message over the UART and
queued it for LoRa; seeing `1`…`10` on another radio confirms the RF side.

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
