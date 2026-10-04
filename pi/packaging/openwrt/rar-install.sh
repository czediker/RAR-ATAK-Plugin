#!/bin/sh
# Installs or upgrades the RAR radio services on an openMANET (OpenWrt) node
# from an unpacked release: usr/bin/, etc/init.d/ and etc/config/rar.default
# next to this script.
#
#   sh rar-install.sh                 install / upgrade; keep /etc/config/rar
#                                     (sections added in new releases are filled in)
#   sh rar-install.sh --reset-config  replace /etc/config/rar with the release
#                                     defaults, keeping this radio's own settings
#                                     (HaLow interface, serial port, baud, ...)
#
# An existing /etc/config/rar is always backed up to /etc/config/rar.bak.
set -eu

ROOT=${RAR_ROOT:-} # install under another root (testing only)
HERE=$(cd "$(dirname "$0")" && pwd)
CONF_DIR=$ROOT/etc/config
CONF=$CONF_DIR/rar
UCI="uci -c $CONF_DIR"

# Settings that describe this radio rather than the software release;
# --reset-config carries them over from the old file.
KEEP="bridge.halow_iface bridge.mesh_iface bridge.serial_device bridge.baud bridge.channel bridge.hop_limit bridge.mcast_iface global.debug"

RESET=0
for a in "$@"; do
	case "$a" in
	--reset-config) RESET=1 ;;
	-h | --help)
		sed -n '2,13p' "$0"
		exit 0
		;;
	*)
		echo "unknown option: $a (try --help)" >&2
		exit 2
		;;
	esac
done

service() { # <name> <action>
	[ -n "${RAR_NO_SERVICES:-}" ] && return 0
	[ -x "$ROOT/etc/init.d/$1" ] || return 0
	"$ROOT/etc/init.d/$1" "$2" || true
}

for f in usr/bin/rar-bridge usr/bin/rar-led usr/bin/rar-meshtest etc/init.d/rar-bridge etc/init.d/rar-led etc/config/rar.default; do
	[ -f "$HERE/$f" ] || {
		echo "missing $HERE/$f: run this from an unpacked release" >&2
		exit 1
	}
done

echo "Stopping rar-bridge and rar-led"
service rar-bridge stop
service rar-led stop

echo "Installing programs and service scripts"
mkdir -p "$ROOT/usr/bin" "$ROOT/etc/init.d" "$CONF_DIR"
for b in rar-bridge rar-led rar-meshtest; do
	cp "$HERE/usr/bin/$b" "$ROOT/usr/bin/$b.new"
	chmod 0755 "$ROOT/usr/bin/$b.new"
	mv "$ROOT/usr/bin/$b.new" "$ROOT/usr/bin/$b"
done
# Strip Windows line endings: a release built from a Windows checkout may
# carry them, and OpenWrt cannot run such scripts.
for s in rar-bridge rar-led; do
	tr -d '\r' <"$HERE/etc/init.d/$s" >"$ROOT/etc/init.d/$s"
	chmod 0755 "$ROOT/etc/init.d/$s"
done
tr -d '\r' <"$HERE/etc/config/rar.default" >"$CONF_DIR/rar.default"

if [ ! -f "$CONF" ]; then
	echo "Creating $CONF from the release defaults"
	cp "$CONF_DIR/rar.default" "$CONF"
elif [ "$RESET" = 1 ]; then
	echo "Resetting $CONF to the release defaults (old file: $CONF.bak)"
	cp "$CONF" "$CONF.bak"
	old=$(mktemp -d)
	tr -d '\r' <"$CONF" >"$old/rar"
	cp "$CONF_DIR/rar.default" "$CONF"
	for k in $KEEP; do
		if v=$($UCI -q -c "$old" get "rar.$k"); then
			$UCI set "rar.$k=$v"
			echo "  kept $k=$v"
		fi
	done
	rm -rf "$old"
else
	echo "Keeping $CONF (backup: $CONF.bak)"
	cp "$CONF" "$CONF.bak"
	if ! $UCI -q get rar.global >/dev/null; then
		$UCI set rar.global=global
		$UCI set rar.global.debug=0
		echo "  added rar.global.debug=0"
	fi
	if [ "$($UCI -q get rar.led.active_low || true)" = 1 ]; then
		echo "  NOTE: rar.led.active_low=1 (common-anode LED). For a common-cathode LED:"
		echo "        uci set rar.led.active_low=0; uci commit rar"
	fi
fi
$UCI commit rar

echo "Enabling and starting rar-led and rar-bridge"
for s in rar-led rar-bridge; do
	service "$s" enable
	service "$s" restart
done

echo
echo "Installed. Current settings ($CONF):"
$UCI show rar
echo
echo "Debug logging: uci set rar.global.debug=1; uci commit rar; logread -f -e rar"
