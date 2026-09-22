#!/bin/sh

set -eu

PROGRAM=nextnet
BEGIN_MARKER="# BEGIN nextnet"
END_MARKER="# END nextnet"
MEDIA_ROOT=${NEXTNET_MEDIA_ROOT:-/media/fat}
LINUX_DIR="$MEDIA_ROOT/linux"
STARTUP_FILE="$LINUX_DIR/user-startup.sh"
LOCK_FILE=${NEXTNET_LOCK_FILE:-/tmp/nextnet.lock}

fail() {
	printf '%s: %s\n' "$PROGRAM" "$*" >&2
	exit 1
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

if [ "$MEDIA_ROOT" = "/media/fat" ] && [ "$(id -u)" -ne 0 ]; then
	fail "run this uninstaller as root on MiSTer"
fi

[ ! -L "$STARTUP_FILE" ] || fail "refusing to replace symbolic link: $STARTUP_FILE"
[ ! -e "$STARTUP_FILE" ] || [ -f "$STARTUP_FILE" ] || fail "not a regular file: $STARTUP_FILE"

if [ -f "$STARTUP_FILE" ]; then
	begin_count=$(marker_count "$BEGIN_MARKER" "$STARTUP_FILE")
	end_count=$(marker_count "$END_MARKER" "$STARTUP_FILE")
	[ "$begin_count" -eq "$end_count" ] || fail "unmatched nextnet markers in $STARTUP_FILE"
	if [ "$begin_count" -gt 0 ]; then
		TEMP_STARTUP=$(mktemp "$LINUX_DIR/.user-startup.sh.nextnet.XXXXXX")
		trap 'rm -f "$TEMP_STARTUP"' EXIT HUP INT TERM
		remove_startup_blocks "$STARTUP_FILE" "$TEMP_STARTUP" || fail "invalid nextnet block in $STARTUP_FILE"
		chmod 755 "$TEMP_STARTUP"
		mv -f "$TEMP_STARTUP" "$STARTUP_FILE"
		trap - EXIT HUP INT TERM
		printf 'Removed automatic startup from %s\n' "$STARTUP_FILE"
	fi
fi

running_pid=
if [ -r "$LOCK_FILE" ]; then
	IFS= read -r lock_pid < "$LOCK_FILE" || lock_pid=
	case $lock_pid in
		''|*[!0-9]*) ;;
		*)
			if kill -0 "$lock_pid" 2>/dev/null; then
				running_exe=$(readlink "/proc/$lock_pid/exe" 2>/dev/null || true)
				case $running_exe in
					*/nextnet|*/nextnet\ \(deleted\)) running_pid=$lock_pid ;;
					*) printf 'Not stopping PID %s because it is not a nextnet binary\n' "$lock_pid" >&2 ;;
				esac
			fi
			;;
	esac
fi

if [ -n "$running_pid" ]; then
	kill "$running_pid"
	remaining=5
	while kill -0 "$running_pid" 2>/dev/null && [ "$remaining" -gt 0 ]; do
		sleep 1
		remaining=$((remaining - 1))
	done
	if kill -0 "$running_pid" 2>/dev/null; then
		fail "PID $running_pid did not stop; installed files were left in place"
	fi
	printf 'Stopped nextnet PID %s\n' "$running_pid"
fi

printf 'Disabled nextnet; program files were left in place\n'
