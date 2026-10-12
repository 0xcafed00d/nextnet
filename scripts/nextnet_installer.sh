#!/bin/sh

set -eu

PROGRAM=nextnet-installer
BEGIN_MARKER="# BEGIN nextnet"
END_MARKER="# END nextnet"
MEDIA_ROOT=${NEXTNET_MEDIA_ROOT:-/media/fat}
NEXTNET_DIR="$MEDIA_ROOT/nextnet"
BINARY="$NEXTNET_DIR/nextnet"
LINUX_DIR="$MEDIA_ROOT/linux"
STARTUP_FILE="$LINUX_DIR/user-startup.sh"
STARTUP_BACKUP="$STARTUP_FILE.nextnet.bak"
LOCK_FILE=${NEXTNET_LOCK_FILE:-/tmp/nextnet.lock}
TEMP_STARTUP=

fail() {
	printf '%s: %s\n' "$PROGRAM" "$*" >&2
	exit 1
}

usage() {
	printf 'Usage: %s [--install | --uninstall | --help]\n' "$0"
}

cleanup() {
	if [ -n "$TEMP_STARTUP" ]; then
		rm -f "$TEMP_STARTUP"
	fi
}

trap cleanup EXIT HUP INT TERM

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

shell_quote() {
	value=$1
	escaped=$(printf '%s' "$value" | sed "s/'/'\\\\''/g")
	printf "'%s'" "$escaped"
}

write_startup_block() {
	file=$1
	quoted_binary=$(shell_quote "$BINARY")
	if [ -s "$file" ]; then
		printf '\n' >> "$file"
	fi
	printf '%s\n' "$BEGIN_MARKER" >> "$file"
	printf 'if [ -x %s ]; then\n' "$quoted_binary" >> "$file"
	printf '    %s -device /dev/ttyS1 -baud 115200 </dev/null >>/tmp/nextnet.log 2>&1 &\n' "$quoted_binary" >> "$file"
	printf 'fi\n' >> "$file"
	printf '%s\n' "$END_MARKER" >> "$file"
}

check_startup_file() {
	[ ! -L "$STARTUP_FILE" ] || fail "refusing to replace symbolic link: $STARTUP_FILE"
	[ ! -e "$STARTUP_FILE" ] || [ -f "$STARTUP_FILE" ] || fail "not a regular file: $STARTUP_FILE"

	STARTUP_BEGIN_COUNT=0
	if [ -f "$STARTUP_FILE" ]; then
		STARTUP_BEGIN_COUNT=$(marker_count "$BEGIN_MARKER" "$STARTUP_FILE")
		end_count=$(marker_count "$END_MARKER" "$STARTUP_FILE")
		[ "$STARTUP_BEGIN_COUNT" -eq "$end_count" ] ||
			fail "unmatched nextnet markers in $STARTUP_FILE"
	fi
}

make_startup_without_nextnet() {
	TEMP_STARTUP=$(mktemp "$LINUX_DIR/.user-startup.sh.nextnet.XXXXXX")
	if [ -f "$STARTUP_FILE" ]; then
		remove_startup_blocks "$STARTUP_FILE" "$TEMP_STARTUP" ||
			fail "invalid nextnet block in $STARTUP_FILE"
	else
		printf '#!/bin/sh\n' > "$TEMP_STARTUP"
	fi
}

find_running_pid() {
	RUNNING_PID=
	if [ ! -r "$LOCK_FILE" ]; then
		return
	fi

	IFS= read -r lock_pid < "$LOCK_FILE" || lock_pid=
	case $lock_pid in
		''|*[!0-9]*) return ;;
	esac
	if ! kill -0 "$lock_pid" 2>/dev/null; then
		return
	fi

	running_exe=$(readlink "/proc/$lock_pid/exe" 2>/dev/null || true)
	case $running_exe in
		*/nextnet|*/nextnet\ \(deleted\)) RUNNING_PID=$lock_pid ;;
		*) printf 'Not using PID %s because it is not a nextnet binary\n' "$lock_pid" >&2 ;;
	esac
}

install_nextnet() {
	[ -f "$BINARY" ] || fail "nextnet binary not found: $BINARY"
	mkdir -p "$LINUX_DIR"
	check_startup_file
	[ ! -L "$STARTUP_BACKUP" ] || fail "refusing to replace symbolic link: $STARTUP_BACKUP"

	if [ -f "$STARTUP_FILE" ] && [ ! -e "$STARTUP_BACKUP" ]; then
		cp "$STARTUP_FILE" "$STARTUP_BACKUP"
	fi

	make_startup_without_nextnet
	write_startup_block "$TEMP_STARTUP"
	chmod 755 "$TEMP_STARTUP"
	chmod +x "$BINARY"
	mv -f "$TEMP_STARTUP" "$STARTUP_FILE"
	TEMP_STARTUP=

	printf 'Configured %s in place\n' "$BINARY"
	printf 'Enabled automatic startup in %s\n' "$STARTUP_FILE"

	find_running_pid
	if [ "${NEXTNET_NO_START:-0}" = "1" ]; then
		printf 'Immediate startup skipped (NEXTNET_NO_START=1)\n'
	elif [ -n "$RUNNING_PID" ]; then
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
}

uninstall_nextnet() {
	check_startup_file

	if [ "$STARTUP_BEGIN_COUNT" -gt 0 ]; then
		make_startup_without_nextnet
		chmod 755 "$TEMP_STARTUP"
		mv -f "$TEMP_STARTUP" "$STARTUP_FILE"
		TEMP_STARTUP=
		printf 'Removed automatic startup from %s\n' "$STARTUP_FILE"
	else
		printf 'Automatic startup was not enabled\n'
	fi

	find_running_pid
	if [ -n "$RUNNING_PID" ]; then
		kill "$RUNNING_PID"
		remaining=5
		while kill -0 "$RUNNING_PID" 2>/dev/null && [ "$remaining" -gt 0 ]; do
			sleep 1
			remaining=$((remaining - 1))
		done
		if kill -0 "$RUNNING_PID" 2>/dev/null; then
			fail "PID $RUNNING_PID did not stop; installed files were left in place"
		fi
		printf 'Stopped nextnet PID %s\n' "$RUNNING_PID"
	fi

	printf 'Disabled nextnet; program files were left in %s\n' "$NEXTNET_DIR"
}

prompt_action() {
	printf '\nNEXTNET installer\n\n'
	printf '  1) Install NEXTNET\n'
	printf '  2) Uninstall NEXTNET\n'
	printf '  3) Exit without making changes\n\n'
	printf 'Select an option [1-3]: '
	IFS= read -r selection || fail "no selection received"
	case $selection in
		1|i|I|install|INSTALL) ACTION=install ;;
		2|u|U|uninstall|UNINSTALL) ACTION=uninstall ;;
		3|q|Q|quit|QUIT|'') ACTION=exit ;;
		*) fail "invalid selection: $selection" ;;
	esac
}

case $# in
	0) ACTION=prompt ;;
	1)
		case $1 in
			--install) ACTION=install ;;
			--uninstall) ACTION=uninstall ;;
			-h|--help)
				usage
				exit 0
				;;
			*)
				usage >&2
				exit 2
				;;
		esac
		;;
	*)
		usage >&2
		exit 2
		;;
esac

if [ "$MEDIA_ROOT" = "/media/fat" ] && [ "$(id -u)" -ne 0 ]; then
	fail "run this installer as root on MiSTer"
fi

if [ "$ACTION" = prompt ]; then
	prompt_action
fi

case $ACTION in
	install) install_nextnet ;;
	uninstall) uninstall_nextnet ;;
	exit) printf 'No changes made\n' ;;
esac
