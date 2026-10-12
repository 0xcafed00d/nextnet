#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nextnet-enable-test.XXXXXX")
trap 'rm -rf "$TEST_DIR"' EXIT HUP INT TERM

MEDIA_ROOT="$TEST_DIR/media/fat"
STARTUP_FILE="$MEDIA_ROOT/linux/user-startup.sh"
BINARY="$MEDIA_ROOT/nextnet/nextnet"
LOCK_FILE="$TEST_DIR/nextnet.lock"
ENABLE_SCRIPT="$SCRIPT_DIR/nextnet_enable.sh"

mkdir -p "$MEDIA_ROOT/linux" "$MEDIA_ROOT/nextnet"
printf '#!/bin/sh\n# user setting\nexport KEEP_THIS=yes\n' > "$STARTUP_FILE"
cp "$STARTUP_FILE" "$TEST_DIR/startup.original"
printf '#!/bin/sh\nexit 0\n' > "$BINARY"
chmod 644 "$BINARY"

run_enable_script() {
	NEXTNET_MEDIA_ROOT="$MEDIA_ROOT" \
	NEXTNET_LOCK_FILE="$LOCK_FILE" \
	NEXTNET_NO_START=1 \
		sh "$ENABLE_SCRIPT" "$@"
}

run_enable_script --enable >/dev/null
run_enable_script --enable >/dev/null

[ -x "$BINARY" ]
[ "$(grep -F -x -c '# BEGIN nextnet' "$STARTUP_FILE")" -eq 1 ]
[ "$(grep -F -x -c '# END nextnet' "$STARTUP_FILE")" -eq 1 ]
grep -F -q 'export KEEP_THIS=yes' "$STARTUP_FILE"
grep -F -q "'$BINARY' -device /dev/ttyS1 -baud 115200" "$STARTUP_FILE"
sh -n "$STARTUP_FILE"
cmp "$TEST_DIR/startup.original" "$STARTUP_FILE.nextnet.bak"

run_enable_script --disable >/dev/null

[ -x "$BINARY" ]
[ "$(grep -F -x -c '# BEGIN nextnet' "$STARTUP_FILE" || true)" -eq 0 ]
[ "$(grep -F -x -c '# END nextnet' "$STARTUP_FILE" || true)" -eq 0 ]
grep -F -q 'export KEEP_THIS=yes' "$STARTUP_FILE"
cmp "$TEST_DIR/startup.original" "$STARTUP_FILE"

printf '1\n' | NEXTNET_MEDIA_ROOT="$MEDIA_ROOT" \
	NEXTNET_LOCK_FILE="$LOCK_FILE" NEXTNET_NO_START=1 \
	sh "$ENABLE_SCRIPT" >/dev/null
[ "$(grep -F -x -c '# BEGIN nextnet' "$STARTUP_FILE")" -eq 1 ]

printf '2\n' | NEXTNET_MEDIA_ROOT="$MEDIA_ROOT" \
	NEXTNET_LOCK_FILE="$LOCK_FILE" \
	sh "$ENABLE_SCRIPT" >/dev/null
[ "$(grep -F -x -c '# BEGIN nextnet' "$STARTUP_FILE" || true)" -eq 0 ]

printf '3\n' | NEXTNET_MEDIA_ROOT="$MEDIA_ROOT" \
	NEXTNET_LOCK_FILE="$LOCK_FILE" \
	sh "$ENABLE_SCRIPT" | grep -F -q 'No changes made'

if run_enable_script --install >/dev/null 2>&1 ||
	run_enable_script --uninstall >/dev/null 2>&1 ||
	run_enable_script --unknown >/dev/null 2>&1; then
	printf 'enable script accepted an obsolete or unknown option\n' >&2
	exit 1
fi

MALFORMED_ROOT="$TEST_DIR/malformed/fat"
MALFORMED_STARTUP="$MALFORMED_ROOT/linux/user-startup.sh"
mkdir -p "$MALFORMED_ROOT/linux" "$MALFORMED_ROOT/nextnet"
cp "$BINARY" "$MALFORMED_ROOT/nextnet/nextnet"
printf '#!/bin/sh\n# BEGIN nextnet\nkeep-me\n' > "$MALFORMED_STARTUP"
cp "$MALFORMED_STARTUP" "$TEST_DIR/malformed.before"
if NEXTNET_MEDIA_ROOT="$MALFORMED_ROOT" NEXTNET_LOCK_FILE="$LOCK_FILE" \
	NEXTNET_NO_START=1 sh "$ENABLE_SCRIPT" --enable >/dev/null 2>&1; then
	printf 'enable script accepted unmatched startup markers\n' >&2
	exit 1
fi
cmp "$TEST_DIR/malformed.before" "$MALFORMED_STARTUP"

sh -n "$ENABLE_SCRIPT"
sh "$ENABLE_SCRIPT" --help | grep -F -q -- '--enable | --disable'
printf 'enable script tests passed\n'
