#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nextnet-version-test.XXXXXX")
trap 'rm -rf "$TEST_DIR"' EXIT HUP INT TERM
REPOSITORY="$TEST_DIR/repository"

assert_version() {
	want=$1
	got=$(sh "$SCRIPT_DIR/version.sh" "$REPOSITORY")
	if [ "$got" != "$want" ]; then
		printf 'version = %s, want %s\n' "$got" "$want" >&2
		exit 1
	fi
}

mkdir -p "$REPOSITORY"
git -C "$REPOSITORY" init -q
git -C "$REPOSITORY" config user.name nextnet-test
git -C "$REPOSITORY" config user.email nextnet-test@example.invalid
printf 'initial\n' > "$REPOSITORY/tracked.txt"
git -C "$REPOSITORY" add tracked.txt
git -C "$REPOSITORY" commit -q -m initial

SHORT_HASH=$(git -C "$REPOSITORY" rev-parse --short HEAD)
assert_version "$SHORT_HASH"

git -C "$REPOSITORY" tag v1.2.3
assert_version v1.2.3

printf 'modified\n' >> "$REPOSITORY/tracked.txt"
assert_version "$SHORT_HASH[dirty]"

git -C "$REPOSITORY" restore tracked.txt
printf 'untracked\n' > "$REPOSITORY/untracked.txt"
assert_version "$SHORT_HASH[dirty]"
rm -f "$REPOSITORY/untracked.txt"

printf 'staged\n' >> "$REPOSITORY/tracked.txt"
git -C "$REPOSITORY" add tracked.txt
assert_version "$SHORT_HASH[dirty]"
git -C "$REPOSITORY" commit -q -m second

SHORT_HASH=$(git -C "$REPOSITORY" rev-parse --short HEAD)
assert_version "$SHORT_HASH"

mkdir -p "$TEST_DIR/not-a-repository"
if [ "$(sh "$SCRIPT_DIR/version.sh" "$TEST_DIR/not-a-repository")" != unknown ]; then
	printf 'non-Git directory did not produce unknown version\n' >&2
	exit 1
fi

printf 'version tests passed\n'
