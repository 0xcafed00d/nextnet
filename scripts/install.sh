#!/bin/sh

set -eu

PROGRAM=nextnet
BEGIN_MARKER="# BEGIN nextnet"
END_MARKER="# END nextnet"
MEDIA_ROOT=${NEXTNET_MEDIA_ROOT:-/media/fat}
LINUX_DIR="$MEDIA_ROOT/linux"
STARTUP_FILE="$LINUX_DIR/user-startup.sh"
LOCK_FILE=${NEXTNET_LOCK_FILE:-/tmp/nextnet.lock}
SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)

fail() {
	printf '%s: %s\n' "$PROGRAM" "$*" >&2
	exit 1
}

usage() {
	printf 'Usage: %s [path-to-nextnet-binary]\n' "$0" >&2
	exit 2
}

find_binary() {
	if [ "$#" -eq 1 ]; then
		printf '%s\n' "$1"
		return
	fi
	if [ -f "$SCRIPT_DIR/nextnet" ]; then
		printf '%s\n' "$SCRIPT_DIR/nextnet"
		return
	fi
	if [ -f "$SCRIPT_DIR/../dist/nextnet-linux-armv7" ]; then
		printf '%s\n' "$SCRIPT_DIR/../dist/nextnet-linux-armv7"
		return
	fi
	fail "cannot find the ARMv7 binary; pass its path as the only argument"
}

absolute_path() {
	path=$1
	directory=$(CDPATH= cd "$(dirname "$path")" && pwd)
	printf '%s/%s\n' "$directory" "$(basename "$path")"
}

shell_quote() {
	value=$1
	escaped=$(printf '%s' "$value" | sed "s/'/'\\\\''/g")
	printf "'%s'" "$escaped"
}

marker_count() {
	marker=$1
	file=$2
	grep -F -x -c "$marker" "$file" 2>/dev/null || true
}

remove_startup_blocks() {
	input=$1
	output=$2
	awk -v begin="$BEGIN_MARKER" -v end="$END_MARKER" '
		BEGIN { inside = 0; blanks = 0 }
		{
			sub(/\r$/, "")
			if ($0 == begin) {
				if (inside) exit 2
				inside = 1
				next
			}
			if ($0 == end) {
				if (!inside) exit 3
				inside = 0
				next
			}
			if (!inside) {
				if ($0 == "") {
					blanks++
					next
				}
				while (blanks > 0) {
					print ""
					blanks--
				}
				print
			}
		}
		END { if (inside) exit 4 }
	' "$input" > "$output"
}

write_startup_block() {
	file=$1
	binary=$2
	quoted_binary=$(shell_quote "$binary")
	if [ -s "$file" ]; then
		printf '\n' >> "$file"
	fi
	printf '%s\n' "$BEGIN_MARKER" >> "$file"
	printf 'if [ -x %s ]; then\n' "$quoted_binary" >> "$file"
	printf '    %s -device /dev/ttyS1 -baud 115200 </dev/null >>/tmp/nextnet.log 2>&1 &\n' "$quoted_binary" >> "$file"
	printf 'fi\n' >> "$file"
	printf '%s\n' "$END_MARKER" >> "$file"
}

case $# in
	0|1) ;;
	*) usage ;;
esac

if [ "$MEDIA_ROOT" = "/media/fat" ] && [ "$(id -u)" -ne 0 ]; then
	fail "run this installer as root on MiSTer"
fi

BINARY=$(find_binary "$@")
[ -f "$BINARY" ] || fail "binary does not exist: $BINARY"
BINARY=$(absolute_path "$BINARY")
[ ! -L "$STARTUP_FILE" ] || fail "refusing to replace symbolic link: $STARTUP_FILE"
[ ! -e "$STARTUP_FILE" ] || [ -f "$STARTUP_FILE" ] || fail "not a regular file: $STARTUP_FILE"
STARTUP_BACKUP="$STARTUP_FILE.nextnet.bak"
[ ! -L "$STARTUP_BACKUP" ] || fail "refusing to replace symbolic link: $STARTUP_BACKUP"

mkdir -p "$LINUX_DIR"

if [ -f "$STARTUP_FILE" ]; then
	begin_count=$(marker_count "$BEGIN_MARKER" "$STARTUP_FILE")
	end_count=$(marker_count "$END_MARKER" "$STARTUP_FILE")
	[ "$begin_count" -eq "$end_count" ] || fail "unmatched nextnet markers in $STARTUP_FILE"
fi

TEMP_STARTUP=
cleanup() {
	rm -f "$TEMP_STARTUP"
}
trap cleanup EXIT HUP INT TERM
TEMP_STARTUP=$(mktemp "$LINUX_DIR/.user-startup.sh.nextnet.XXXXXX")

chmod +x "$BINARY"

if [ -f "$STARTUP_FILE" ]; then
	if [ ! -e "$STARTUP_BACKUP" ]; then
		cp "$STARTUP_FILE" "$STARTUP_BACKUP"
	fi
	remove_startup_blocks "$STARTUP_FILE" "$TEMP_STARTUP" || fail "invalid nextnet block in $STARTUP_FILE"
else
	printf '#!/bin/sh\n' > "$TEMP_STARTUP"
fi

write_startup_block "$TEMP_STARTUP" "$BINARY"
chmod 755 "$TEMP_STARTUP"

mv -f "$TEMP_STARTUP" "$STARTUP_FILE"

already_running=false
if [ -r "$LOCK_FILE" ]; then
	IFS= read -r lock_pid < "$LOCK_FILE" || lock_pid=
	case $lock_pid in
		''|*[!0-9]*) ;;
		*)
			if kill -0 "$lock_pid" 2>/dev/null; then
				running_exe=$(readlink "/proc/$lock_pid/exe" 2>/dev/null || true)
				case $running_exe in
					*/nextnet|*/nextnet\ \(deleted\)) already_running=true ;;
				esac
			fi
			;;
	esac
fi

printf 'Configured %s in place\n' "$BINARY"
printf 'Enabled automatic startup in %s\n' "$STARTUP_FILE"

if [ "${NEXTNET_NO_START:-0}" = "1" ]; then
	printf 'Immediate startup skipped (NEXTNET_NO_START=1)\n'
elif [ "$already_running" = "true" ]; then
	printf 'nextnet is already running; the new binary will be used after restart or reboot\n'
else
	"$BINARY" -device /dev/ttyS1 -baud 115200 </dev/null >>/tmp/nextnet.log 2>&1 &
	started_pid=$!
	sleep 1
	if kill -0 "$started_pid" 2>/dev/null; then
		printf 'Started nextnet as PID %s\n' "$started_pid"
	else
		fail "nextnet did not remain running; inspect /tmp/nextnet.log"
	fi
fi

printf 'Log: /tmp/nextnet.log\n'
