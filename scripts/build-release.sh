#!/bin/sh

set -eu

PROGRAM=nextnet-release
SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
PROJECT_DIR=$(CDPATH= cd "$SCRIPT_DIR/.." && pwd)
DIST_DIR="$PROJECT_DIR/dist"

fail() {
	printf '%s: %s\n' "$PROGRAM" "$*" >&2
	exit 1
}

case $# in
	0) ;;
	*)
		printf 'Usage: %s\n' "$0" >&2
		exit 2
		;;
esac

for command in git go sed zip sha256sum mktemp touch; do
	command -v "$command" >/dev/null 2>&1 || fail "required command not found: $command"
done

RELEASE_VERSION=$(sh "$SCRIPT_DIR/version.sh" "$PROJECT_DIR")
[ "$RELEASE_VERSION" != unknown ] || fail "cannot build a versioned release outside a Git checkout"
ARCHIVE_VERSION=$(printf '%s' "$RELEASE_VERSION" |
	sed -e 's/\[dirty\]/-dirty/g' -e 's/[^A-Za-z0-9._-]/-/g')
[ -n "$ARCHIVE_VERSION" ] || fail "derived version is not usable in an archive name"

if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
	if command -v git >/dev/null 2>&1 && git -C "$PROJECT_DIR" log -1 --format=%ct >/dev/null 2>&1; then
		SOURCE_DATE_EPOCH=$(git -C "$PROJECT_DIR" log -1 --format=%ct)
	else
		SOURCE_DATE_EPOCH=315532800
	fi
fi
case $SOURCE_DATE_EPOCH in
	''|*[!0-9]*) fail "SOURCE_DATE_EPOCH must be a non-negative integer" ;;
esac
if [ "$SOURCE_DATE_EPOCH" -lt 315532800 ]; then
	SOURCE_DATE_EPOCH=315532800
fi

mkdir -p "$DIST_DIR"
STAGE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nextnet-release.XXXXXX")
BUNDLE_DIR="$STAGE_DIR/nextnet-mister"
ARCHIVE_NAME="nextnet-mister-armv7-$ARCHIVE_VERSION.zip"
CHECKSUM_NAME="$ARCHIVE_NAME.sha256"

cleanup() {
	rm -f "$BUNDLE_DIR/nextnet" "$BUNDLE_DIR/install.sh" \
		"$BUNDLE_DIR/uninstall.sh" "$BUNDLE_DIR/README.md" \
		"$BUNDLE_DIR/examples/esp-reset.bas" \
		"$STAGE_DIR/$ARCHIVE_NAME"
	rmdir "$BUNDLE_DIR/examples" 2>/dev/null || true
	rmdir "$BUNDLE_DIR" 2>/dev/null || true
	rmdir "$STAGE_DIR" 2>/dev/null || true
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$BUNDLE_DIR/examples"

CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
	go build -C "$PROJECT_DIR" -buildvcs=false -trimpath \
	-ldflags="-s -w -X nextnet/internal/version.Version=$RELEASE_VERSION" \
	-o "$BUNDLE_DIR/nextnet" ./cmd/nextnet

cp "$SCRIPT_DIR/install.sh" "$SCRIPT_DIR/uninstall.sh" "$BUNDLE_DIR/"
cp "$PROJECT_DIR/README.md" "$BUNDLE_DIR/README.md"
cp "$PROJECT_DIR/examples/esp-reset.bas" "$BUNDLE_DIR/examples/"
chmod 755 "$BUNDLE_DIR/nextnet" "$BUNDLE_DIR/install.sh" "$BUNDLE_DIR/uninstall.sh"
chmod 644 "$BUNDLE_DIR/README.md" "$BUNDLE_DIR/examples/esp-reset.bas"

touch -d "@$SOURCE_DATE_EPOCH" "$BUNDLE_DIR" "$BUNDLE_DIR/nextnet" \
	"$BUNDLE_DIR/install.sh" "$BUNDLE_DIR/uninstall.sh" "$BUNDLE_DIR/README.md" \
	"$BUNDLE_DIR/examples" "$BUNDLE_DIR/examples/esp-reset.bas"

(
	cd "$STAGE_DIR"
	TZ=UTC zip -X -q "$ARCHIVE_NAME" \
		nextnet-mister/ \
		nextnet-mister/nextnet \
		nextnet-mister/install.sh \
		nextnet-mister/uninstall.sh \
		nextnet-mister/README.md \
		nextnet-mister/examples/ \
		nextnet-mister/examples/esp-reset.bas
)

mv -f "$STAGE_DIR/$ARCHIVE_NAME" "$DIST_DIR/$ARCHIVE_NAME"
(
	cd "$DIST_DIR"
	sha256sum "$ARCHIVE_NAME" > "$CHECKSUM_NAME"
)

printf 'Created %s\n' "$DIST_DIR/$ARCHIVE_NAME"
printf 'Created %s\n' "$DIST_DIR/$CHECKSUM_NAME"
