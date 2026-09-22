#!/bin/sh

set -eu

PROGRAM=nextnet-version
SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
DEFAULT_PROJECT_DIR=$(CDPATH= cd "$SCRIPT_DIR/.." && pwd)

usage() {
	printf 'Usage: %s [project-directory]\n' "$0" >&2
	exit 2
}

case $# in
	0) PROJECT_DIR=$DEFAULT_PROJECT_DIR ;;
	1) PROJECT_DIR=$1 ;;
	*) usage ;;
esac

if ! command -v git >/dev/null 2>&1 ||
	! git -C "$PROJECT_DIR" rev-parse --verify HEAD >/dev/null 2>&1; then
	printf '%s\n' unknown
	exit 0
fi

SHORT_HASH=$(git -C "$PROJECT_DIR" rev-parse --short HEAD) || {
	printf '%s: cannot determine Git commit\n' "$PROGRAM" >&2
	exit 1
}

if [ -n "$(git -C "$PROJECT_DIR" status --porcelain --untracked-files=normal)" ]; then
	printf '%s[dirty]\n' "$SHORT_HASH"
	exit 0
fi

TAG=$(git -C "$PROJECT_DIR" describe --tags --exact-match HEAD 2>/dev/null || true)
if [ -n "$TAG" ]; then
	printf '%s\n' "$TAG"
else
	printf '%s\n' "$SHORT_HASH"
fi
