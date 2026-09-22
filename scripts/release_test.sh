#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/nextnet-release-test.XXXXXX")
trap 'rm -rf "$TEST_DIR"' EXIT HUP INT TERM
REPOSITORY="$TEST_DIR/repository"
REMOTE="$TEST_DIR/remote.git"
FAKE_BIN="$TEST_DIR/bin"
MAKE_LOG="$TEST_DIR/make.log"

mkdir -p "$REPOSITORY/scripts" "$FAKE_BIN"
cp "$SCRIPT_DIR/release.sh" "$SCRIPT_DIR/version.sh" "$REPOSITORY/scripts/"
printf 'project\n' > "$REPOSITORY/README.md"

git init -q --bare "$REMOTE"
git -C "$REPOSITORY" init -q
git -C "$REPOSITORY" config user.name nextnet-test
git -C "$REPOSITORY" config user.email nextnet-test@example.invalid
git -C "$REPOSITORY" remote add origin "$REMOTE"
git -C "$REPOSITORY" add README.md scripts
git -C "$REPOSITORY" commit -q -m initial

printf '%s\n' \
	'#!/bin/sh' \
	'set -eu' \
	'printf "%s\\n" "$*" >> "$RELEASE_TEST_MAKE_LOG"' \
	'last_argument=' \
	'for argument do last_argument=$argument; done' \
	'if [ "${RELEASE_TEST_FAIL_TARGET:-}" != "" ] && [ "$last_argument" = "$RELEASE_TEST_FAIL_TARGET" ]; then exit 1; fi' \
	'exit 0' > "$FAKE_BIN/make"
chmod 755 "$FAKE_BIN/make"

PATH="$FAKE_BIN:$PATH" RELEASE_TEST_MAKE_LOG="$MAKE_LOG" \
	sh "$REPOSITORY/scripts/release.sh" v1.2.3 >/dev/null 2>&1

HEAD=$(git -C "$REPOSITORY" rev-parse HEAD)
[ "$(git -C "$REPOSITORY" rev-parse 'refs/tags/v1.2.3^{}')" = "$HEAD" ]
[ "$(git --git-dir="$REMOTE" rev-parse 'refs/tags/v1.2.3^{}')" = "$HEAD" ]
[ "$(git -C "$REPOSITORY" cat-file -t refs/tags/v1.2.3)" = tag ]
[ "$(sh "$REPOSITORY/scripts/version.sh" "$REPOSITORY")" = v1.2.3 ]
for target in test clean build package-mister release-zip; do
	grep -Eq "(^| )$target$" "$MAKE_LOG"
done

assert_dirty_rejected() {
	version=$1
	description=$2
	if PATH="$FAKE_BIN:$PATH" RELEASE_TEST_MAKE_LOG="$MAKE_LOG" \
		sh "$REPOSITORY/scripts/release.sh" "$version" >/dev/null 2>&1; then
		printf 'release accepted %s changes\n' "$description" >&2
		exit 1
	fi
	if git -C "$REPOSITORY" rev-parse --verify --quiet "refs/tags/$version" >/dev/null; then
		printf '%s release created a local tag\n' "$description" >&2
		exit 1
	fi
}

printf 'unstaged\n' >> "$REPOSITORY/README.md"
assert_dirty_rejected v1.2.4 unstaged
git -C "$REPOSITORY" restore README.md

printf 'staged\n' >> "$REPOSITORY/README.md"
git -C "$REPOSITORY" add README.md
assert_dirty_rejected v1.2.5 staged
git -C "$REPOSITORY" restore --staged README.md
git -C "$REPOSITORY" restore README.md

printf 'untracked\n' > "$REPOSITORY/untracked.txt"
assert_dirty_rejected v1.2.6 untracked
rm -f "$REPOSITORY/untracked.txt"

if PATH="$FAKE_BIN:$PATH" RELEASE_TEST_MAKE_LOG="$MAKE_LOG" \
	RELEASE_TEST_FAIL_TARGET=build \
	sh "$REPOSITORY/scripts/release.sh" v1.2.7 >/dev/null 2>&1; then
	printf 'release succeeded after a failed build\n' >&2
	exit 1
fi
if git -C "$REPOSITORY" rev-parse --verify --quiet refs/tags/v1.2.7 >/dev/null; then
	printf 'failed release left its local tag behind\n' >&2
	exit 1
fi
if git --git-dir="$REMOTE" rev-parse --verify --quiet refs/tags/v1.2.7 >/dev/null; then
	printf 'failed release pushed its tag\n' >&2
	exit 1
fi

if PATH="$FAKE_BIN:$PATH" RELEASE_TEST_MAKE_LOG="$MAKE_LOG" \
	sh "$REPOSITORY/scripts/release.sh" not-a-version >/dev/null 2>&1; then
	printf 'release accepted an invalid version\n' >&2
	exit 1
fi

printf 'release tests passed\n'
