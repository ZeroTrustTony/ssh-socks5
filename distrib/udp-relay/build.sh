#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

IMAGE="${BUILD_IMAGE:-golang:1.26.5-alpine}"
OUTPUT="${OUTPUT:-ssh-socks5-udp-relay}"
GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"

if command -v podman >/dev/null 2>&1; then
	CONTAINER=podman
elif command -v docker >/dev/null 2>&1; then
	CONTAINER=docker
else
	echo "error: neither podman nor docker found in PATH" >&2
	exit 1
fi

echo "Building $OUTPUT for $GOOS/$GOARCH using $CONTAINER ($IMAGE)..."

$CONTAINER run --rm \
	-v "$SCRIPT_DIR:/src" \
	-w /src \
	"$IMAGE" \
	sh -c "CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -ldflags='-s -w' -o $OUTPUT ."

echo "Built: $SCRIPT_DIR/$OUTPUT"
