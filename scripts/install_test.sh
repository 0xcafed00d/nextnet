#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nextnet-install-test.XXXXXX")
trap 'rm -rf "$TEST_DIR"' EXIT HUP INT TERM

MEDIA_ROOT="$TEST_DIR/media/fat"
STARTUP_FILE="$MEDIA_ROOT/linux/user-startup.sh"
FAKE_BINARY="$TEST_DIR/nextnet"
LOCK_FILE="$TEST_DIR/nextnet.lock"
BUNDLE_DIR="$TEST_DIR/bundle"

mkdir -p "$MEDIA_ROOT/linux"
printf '#!/bin/sh\n# user setting\nexport KEEP_THIS=yes\n' > "$STARTUP_FILE"
cp "$STARTUP_FILE" "$TEST_DIR/startup.original"
printf '#!/bin/sh\nexit 0\n' > "$FAKE_BINARY"
chmod 755 "$FAKE_BINARY"
mkdir -p "$BUNDLE_DIR"
cp "$FAKE_BINARY" "$BUNDLE_DIR/nextnet"
cp "$SCRIPT_DIR/install.sh" "$SCRIPT_DIR/uninstall.sh" "$BUNDLE_DIR/"

run_install() {
	NEXTNET_MEDIA_ROOT="$MEDIA_ROOT" \
	NEXTNET_LOCK_FILE="$LOCK_FILE" \
	NEXTNET_NO_START=1 \
		sh "$BUNDLE_DIR/install.sh"
}

run_install >/dev/null
run_install >/dev/null

[ -x "$MEDIA_ROOT/linux/nextnet/nextnet" ]
[ -x "$MEDIA_ROOT/linux/nextnet/uninstall.sh" ]
[ "$(grep -F -x -c '# BEGIN nextnet' "$STARTUP_FILE")" -eq 1 ]
[ "$(grep -F -x -c '# END nextnet' "$STARTUP_FILE")" -eq 1 ]
grep -F -q 'export KEEP_THIS=yes' "$STARTUP_FILE"
grep -F -q '"'"$MEDIA_ROOT"'/linux/nextnet/nextnet" -device /dev/ttyS1 -baud 115200' "$STARTUP_FILE"
cmp "$TEST_DIR/startup.original" "$STARTUP_FILE.nextnet.bak"

NEXTNET_MEDIA_ROOT="$MEDIA_ROOT" NEXTNET_LOCK_FILE="$LOCK_FILE" \
	sh "$MEDIA_ROOT/linux/nextnet/uninstall.sh" >/dev/null

[ ! -e "$MEDIA_ROOT/linux/nextnet/nextnet" ]
[ "$(grep -F -x -c '# BEGIN nextnet' "$STARTUP_FILE" || true)" -eq 0 ]
[ "$(grep -F -x -c '# END nextnet' "$STARTUP_FILE" || true)" -eq 0 ]
grep -F -q 'export KEEP_THIS=yes' "$STARTUP_FILE"
cmp "$TEST_DIR/startup.original" "$STARTUP_FILE"

MALFORMED_ROOT="$TEST_DIR/malformed/fat"
MALFORMED_STARTUP="$MALFORMED_ROOT/linux/user-startup.sh"
mkdir -p "$MALFORMED_ROOT/linux"
printf '#!/bin/sh\n# BEGIN nextnet\nkeep-me\n' > "$MALFORMED_STARTUP"
cp "$MALFORMED_STARTUP" "$TEST_DIR/malformed.before"
if NEXTNET_MEDIA_ROOT="$MALFORMED_ROOT" NEXTNET_LOCK_FILE="$LOCK_FILE" \
	NEXTNET_NO_START=1 sh "$SCRIPT_DIR/install.sh" "$FAKE_BINARY" >/dev/null 2>&1; then
	printf 'installer accepted an unmatched startup marker\n' >&2
	exit 1
fi
cmp "$TEST_DIR/malformed.before" "$MALFORMED_STARTUP"
[ ! -e "$MALFORMED_ROOT/linux/nextnet/nextnet" ]

printf 'installer tests passed\n'
