# Hardware setup

## Pin usage on the Pi 4 header

| Physical pin | BCM GPIO | Used by |
|--------------|----------|---------|
| 24, 21, 19, 23 | 8, 9, 10, 11 | WM1302 HAT: HaLow radio SPI0 (CE0, MISO, MOSI, SCLK) |
| 11, 29, 16, 18 | 17, 5, 23, 24 | WM1302 HAT: HaLow radio reset, interrupt, wake, busy (openMANET's `mm610x-spi` overlay) |
| 8, 10 | 14 (TXD), 15 (RXD) | WM1302 HAT's GPS (Quectel L76K), read by openMANET's gpsd as `/dev/ttyAMA0` |
| 32, 22 | 12, 25 | WM1302 HAT's GPS wake / reset (openMANET's `gpsboard` script) |
| **27** | **0 (TXD2)** | → RAK4631 RXD1 (uart2) |
| **28** | **1 (RXD2)** | ← RAK4631 TXD1 (uart2) |
| **36** | **16** | LED red anode (reserved, held off) |
| **38** | **20** | LED green anode (Meshtastic) |
| **40** | **21** | LED blue anode (HaLow) |
| 39 (or any ground) | — | LED common cathode |
| 6, 9, 14, 20, 25, 30, 34 | — | Ground (one to the RAK4631) |

GPIO 16/20/21 avoid the HaLow radio, the HAT's GPS, the UARTs and I²C. They
can be changed with `uci set rar.led.red=...` etc.

## Connecting the RAK4631 (uart2, pins 27/28)

The Pi's primary UART (pins 8/10, `/dev/ttyAMA0`) belongs to the WM1302
HAT's own GPS, a Quectel L76K. Seeed reads it with `cat /dev/ttyAMA0`
([WM1302 Pi HAT wiki](https://wiki.seeedstudio.com/WM1302_Pi_HAT/)), and
openMANET runs gpsd on it. A RAK4631 on those pins fights the GPS for pin
10, so the radio's replies are lost, and gpsd takes the radio's data. The
bridge reports that as `serial port problem: process … (gpsd) has
/dev/ttyAMA0 open`.

The Pi 4 has four more full UARTs (uart2–uart5), each available on one
fixed pair of header pins through a `config.txt` overlay. With the
WM1302 HAT fitted:

| UART | Pins (GPIO) | With the WM1302 HAT |
|------|-------------|---------------------|
| uart2 | 27 / 28 (0 / 1) | **Free**, unless the HAT has an ID EEPROM on them (check below) |
| uart3 | 7 / 29 (4 / 5) | GPIO5 is the HaLow radio's interrupt line |
| uart4 | 24 / 21 (8 / 9) | HaLow radio SPI |
| uart5 | 32 / 33 (12 / 13) | GPIO12 is the HAT GPS's wake line (see the fallback below) |

### 1. Check that pins 27/28 are free

GPIO 0/1 are reserved for a HAT identification EEPROM. On the radio,
before enabling uart2:

```sh
ls /proc/device-tree/hat 2>/dev/null || echo "no HAT EEPROM"
gpioget -c gpiochip0 -b pull-down 0 1      # expect both inactive (low)
gpioget -c gpiochip0 -b pull-up 0 1        # expect both active (high)
```

If there's no HAT EEPROM and the pins follow the pull-down and pull-up,
nothing on the HAT uses them. If `/proc/device-tree/hat` exists or the
pins read high even with the pull-down, the HAT has an EEPROM with
pull-ups there; use the fallback below.

### 2. Enable uart2

Add to the end of `/boot/config.txt` (the boot partition; see
`mount | grep mmcblk0p1`):

```ini
[all]
dtoverlay=uart2
force_eeprom_read=0
```

`force_eeprom_read=0` stops the boot firmware from probing GPIO 0/1 for an
EEPROM, which would otherwise toggle the lines going to the RAK at every
boot. Reboot, then find uart2's device name. uart2's hardware address is
`fe201400`, and it is normally `/dev/ttyAMA2`:

```sh
dmesg | grep fe201400      # fe201400.serial: ttyAMA2 at MMIO 0xfe201400 ...
```

### 3. Test the UART alone, then wire the radio

With the RAK unplugged, join pins 27 and 28 with a jumper wire and run
`rar-meshtest -loopback -serial /dev/ttyAMA2`. On PASS, remove the jumper
and wire the radio (both sides are 3.3 V logic):

* Pi pin 27 (GPIO0, TXD2) → RAK4631 **RXD1** (nRF52 P0.15)
* Pi pin 28 (GPIO1, RXD2) ← RAK4631 **TXD1** (nRF52 P0.16)
* Ground ↔ ground

New installs use `/dev/ttyAMA2`. On an existing install:

```sh
uci set rar.bridge.serial_device='/dev/ttyAMA2'; uci commit rar   # the bridge restarts
```

Leave openMANET's gpsd and the HAT's GPS on `/dev/ttyAMA0` as they are.
uart2 never carries a Linux console, so the console steps below apply only
to the primary UART.

### Fallback: uart5 on pins 32/33 (gives up the HAT's GPS)

If pins 27/28 aren't free, use uart5 (GPIO 12/13). GPIO12 is the HAT GPS's
wake line, so the HAT's GPS has to be switched off:

1. Check that nothing drives GPIO13, before enabling uart5:
   `gpioget -c gpiochip0 -b pull-down 13` then `-b pull-up 13`; it should
   follow both.
2. Stop openMANET's GPS:
   ```sh
   /etc/init.d/gpsboard.init disable; /etc/init.d/gpsboard.init stop
   pkill -f "gpioset.*12=" ; pkill -f "gpioset.*25="
   uci set gpsd.core.enabled=0; uci commit gpsd; /etc/init.d/gpsd stop
   ```
   Then set openMANET's position source to the ATAK phone: in the Web UI
   GPS page, choose **External · EUD (ATAK) via CoT**.
3. Add `dtoverlay=uart5` under `[all]` in `/boot/config.txt` and reboot.
   uart5's hardware address is `fe201a00`, normally `/dev/ttyAMA5`; check
   with `dmesg | grep fe201a00`.
4. Wire Pi pin 32 (GPIO12, TXD5) → RAK RXD1 and Pi pin 33 (GPIO13, RXD5) ←
   RAK TXD1, then set `rar.bridge.serial_device` to that device.

The HAT GPS's wake input stays connected to pin 32 and follows the UART's
transmit line, which idles high and so keeps the unused GPS in standby.

Other ways to free a UART need hardware changes: disconnecting the HAT
GPS's lines from pins 8/10 (see Seeed's schematic for that HAT revision),
or adding an I²C-to-UART bridge chip such as the SC16IS752. The bridge
also works over USB (`/dev/ttyACM0`) where there's room for it.

### Meshtastic settings on the RAK4631

The bridge talks to the radio with the Meshtastic protobuf API, which the
RAK provides on its UART only when the **Serial module runs in PROTO
mode**:

| Setting | Value |
|---------|-------|
| Serial module → enabled | true |
| mode | **PROTO** |
| rxd / txd | 15 / 16 (RXD1 / TXD1) |
| baud | 115200 (must match `rar.bridge.baud`) |
| Position → GPS mode | **NOT_PRESENT** |
| Device → role | **TAK** |

With the Meshtastic CLI over USB, for example:

```sh
meshtastic --set serial.enabled true --set serial.mode PROTO \
           --set serial.rxd 15 --set serial.txd 16 --set serial.baud BAUD_115200 \
           --set position.gps_mode NOT_PRESENT --set device.role TAK
```

**Device role TAK.** On Meshtastic firmware 2.7.x, a radio's ATAK module
runs only when its role is TAK (or TAK_TRACKER). Radios used with the
Meshtastic ATAK plugin are normally set to TAK. Their module compresses the
ATAK data they send, and only a radio running the module decompresses it
for its connected app. With any other role, the RAK hands the bridge
compressed data the bridge can't read. Positions and chat from those users
then never reach your EUD, and the bridge logs `received compressed ATAK
data the radio did not decompress` (and blinks green). Set the role on
every radio in the group. Firmware 2.8.0 and newer no longer compresses
this data.

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

When `rar-bridge` (or `rar-meshtest`) connects over the UART (or USB), the
radio treats it as an app connected over serial:

* **Firmware 2.7.x and older turn Bluetooth off while a serial app is
  connected.** A phone connected over Bluetooth is dropped as soon as the
  bridge connects, and can't reconnect while the bridge runs. If the phone
  drops when the bridge connects, the radio is hearing the Pi. When the
  bridge stops, it tells the radio it is leaving, and Bluetooth comes back
  straight away. Without that message, for example after a power cut or with
  the cable pulled, Bluetooth comes back 15 minutes after the link goes
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

Only for builds without the WM1302 HAT's GPS, using the primary UART for
the RAK. On a Pi 4, `/dev/ttyAMA0` (PL011) drives Bluetooth by default. In the boot
partition's `config.txt` (on OpenWrt usually `/boot/config.txt`):

```ini
enable_uart=1
dtoverlay=disable-bt
```

This does not affect the onboard Wi-Fi access point.

The UART must not also be the Linux console. If it is, kernel messages
and a login prompt go to the RAK4631, and the radio's replies go to the
login prompt, which takes them as keystrokes. Each time that login session
ends, the kernel hangs up the port. The bridge then logs
`serial port was hung up by the kernel` (older versions:
`read: Port has been closed`) and reconnects, over and over. To prevent it:

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
* make sure `/etc/inittab` has no login on `ttyAMA0`, `ttyS0` or `serial0`
  (OpenWrt's `::askconsole:` line follows the kernel console, so it moves
  off the UART once the console is removed).

Reboot, then confirm on the radio that the change took effect:

```sh
cat /proc/cmdline                   # the line the Pi booted with: no console=ttyAMA0 / serial0 / ttyS0
cat /sys/class/tty/console/active   # the kernel's consoles, e.g. "tty1": must not list ttyAMA0
grep -v '^#' /etc/inittab           # no line naming ttyAMA0, ttyS0 or serial0
mount | grep mmcblk0p1              # where the boot partition is mounted (normally /boot)
```

If `/proc/cmdline` still shows the serial console, the edited
`cmdline.txt` isn't the one the Pi boots from. Edit the copy on the boot
partition: `mount /dev/mmcblk0p1 /mnt` if it isn't mounted, then edit
`/mnt/cmdline.txt`. `rar-bridge` also checks for this at startup, and again
whenever the radio is disconnected. Problems appear in the log as
`serial port problem: …`, naming the process that is on the port.

When it's right, `logread -e rar-bridge` reports `radio ready` with the
RAK's node ID.

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
  -> nothing arrived from the radio: check the RAK TXD1 -> Pi RX wire, ...
```

| Received from the radio | Meaning |
|-------------------------|---------|
| 0 bytes | Nothing arrives from the radio. If a phone was dropped from Bluetooth at the same moment (firmware 2.7.x and older), the radio hears the Pi, so only the radio → Pi direction is broken: GPS mode not `NOT_PRESENT`, the RAK TXD1 → Pi RX wire (pin 28 for uart2), or something else driving that pin (the HAT's GPS drives pin 10). Otherwise also check the Serial module settings and the Pi → RAK wire |
| bytes, 0 API frames | The baud rate doesn't match, the Serial module isn't in PROTO mode, or the radio's GPS driver is also using the pins |
| API frames, but no handshake | Data is being lost: another program on the Pi is reading the port (a login console, gpsd) |

To test the Pi's UART on its own, unplug the radio, join the UART's TX and
RX pins with a jumper wire (pins 27 and 28 for uart2), and run the commands
below.

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
