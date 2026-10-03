#!/bin/sh
# Regenerates pi/internal/meshpb from the vendored Meshtastic .proto files in
# pi/proto/meshtastic. Requires `buf` and `protoc-gen-go` on PATH:
#
#   go install github.com/bufbuild/buf/cmd/buf@latest
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#
# To update the protos, copy the files listed in FILES from a checkout of
# https://github.com/meshtastic/protobufs (meshtastic/*.proto) into
# pi/proto/meshtastic, record the upstream commit in UPSTREAM_COMMIT, then run
# this script.
set -eu

HERE=$(cd "$(dirname "$0")/.." && pwd)
MOD=github.com/czediker/rar-atak-plugin/pi
PKG=$MOD/internal/meshpb
FILES="mesh atak portnums channel config device_ui module_config telemetry xmodem field_metadata"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/proto/meshtastic" "$WORK/out"
for f in $FILES; do
	cp "$HERE/proto/meshtastic/$f.proto" "$WORK/proto/meshtastic/"
done

OPTS="paths=import"
for f in $FILES; do
	OPTS="$OPTS,Mmeshtastic/$f.proto=$PKG;meshpb"
done

cat >"$WORK/buf.yaml" <<EOF
version: v2
modules:
  - path: proto
EOF

cat >"$WORK/buf.gen.yaml" <<EOF
version: v2
plugins:
  - local: protoc-gen-go
    out: out
    opt: $OPTS
EOF

(cd "$WORK" && buf generate)

rm -f "$HERE"/internal/meshpb/*.pb.go
cp "$WORK/out/$PKG"/*.pb.go "$HERE/internal/meshpb/"
echo "regenerated $(ls "$HERE"/internal/meshpb/*.pb.go | wc -l) files in internal/meshpb"
