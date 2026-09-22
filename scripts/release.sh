#!/bin/sh

set -eu

PROGRAM=nextnet-release
SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && pwd)
PROJECT_DIR=$(CDPATH= cd "$SCRIPT_DIR/.." && pwd)

fail() {
	printf '%s: %s\n' "$PROGRAM" "$*" >&2
	exit 1
}

usage() {
	printf 'Usage: %s VERSION\n' "$0" >&2
	printf 'Example: %s v1.2.3\n' "$0" >&2
	exit 2
}

case $# in
	1) RELEASE_VERSION=$1 ;;
	*) usage ;;
esac

for command in git grep make; do
	command -v "$command" >/dev/null 2>&1 || fail "required command not found: $command"
done

printf '%s\n' "$RELEASE_VERSION" |
	grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$' ||
	fail "invalid version $RELEASE_VERSION; expected a value such as v1.2.3 or 1.2.3-rc.1"
git check-ref-format "refs/tags/$RELEASE_VERSION" >/dev/null 2>&1 ||
	fail "version is not a valid Git tag: $RELEASE_VERSION"

git -C "$PROJECT_DIR" rev-parse --verify HEAD >/dev/null 2>&1 ||
	fail "project is not a Git checkout with a commit"
BRANCH=$(git -C "$PROJECT_DIR" symbolic-ref --quiet --short HEAD) ||
	fail "HEAD is detached; check out the branch to release first"

require_clean_tree() {
	if [ -n "$(git -C "$PROJECT_DIR" status --porcelain --untracked-files=normal)" ]; then
		fail "working tree has staged, unstaged, or untracked changes"
	fi
}

require_clean_tree

if git -C "$PROJECT_DIR" rev-parse --verify --quiet "refs/tags/$RELEASE_VERSION" >/dev/null; then
	fail "local tag already exists: $RELEASE_VERSION"
fi

REMOTE=$(git -C "$PROJECT_DIR" config --get "branch.$BRANCH.remote" || true)
if [ -z "$REMOTE" ] || [ "$REMOTE" = . ]; then
	if git -C "$PROJECT_DIR" remote get-url origin >/dev/null 2>&1; then
		REMOTE=origin
	else
		for candidate in $(git -C "$PROJECT_DIR" remote); do
			[ -z "$REMOTE" ] || fail "cannot choose a push remote; configure the branch remote or origin"
			REMOTE=$candidate
		done
		[ -n "$REMOTE" ] || fail "cannot choose a push remote; configure the branch remote or origin"
	fi
fi
git -C "$PROJECT_DIR" remote get-url "$REMOTE" >/dev/null 2>&1 ||
	fail "Git remote does not exist: $REMOTE"

REMOTE_TAG=$(git -C "$PROJECT_DIR" ls-remote --refs --tags "$REMOTE" "refs/tags/$RELEASE_VERSION") ||
	fail "cannot query tag $RELEASE_VERSION on remote $REMOTE"
[ -z "$REMOTE_TAG" ] || fail "remote tag already exists on $REMOTE: $RELEASE_VERSION"

printf 'Testing branch %s before tagging...\n' "$BRANCH"
make -C "$PROJECT_DIR" test
require_clean_tree

git -C "$PROJECT_DIR" tag -a "$RELEASE_VERSION" -m "Release $RELEASE_VERSION"
TAG_CREATED=1
TAG_PUSHED=0
cleanup() {
	if [ "$TAG_CREATED" -eq 1 ] && [ "$TAG_PUSHED" -eq 0 ]; then
		git -C "$PROJECT_DIR" tag -d "$RELEASE_VERSION" >/dev/null 2>&1 || true
		TAG_CREATED=0
		printf '%s: removed unpushed local tag %s after failure\n' "$PROGRAM" "$RELEASE_VERSION" >&2
	fi
}
trap cleanup EXIT HUP INT TERM

DETECTED_VERSION=$(sh "$SCRIPT_DIR/version.sh" "$PROJECT_DIR")
[ "$DETECTED_VERSION" = "$RELEASE_VERSION" ] ||
	fail "tagged version resolved as $DETECTED_VERSION instead of $RELEASE_VERSION"

printf 'Building distribution for %s...\n' "$RELEASE_VERSION"
make -C "$PROJECT_DIR" clean
make -C "$PROJECT_DIR" build
make -C "$PROJECT_DIR" package-mister
make -C "$PROJECT_DIR" release-zip

DETECTED_VERSION=$(sh "$SCRIPT_DIR/version.sh" "$PROJECT_DIR")
[ "$DETECTED_VERSION" = "$RELEASE_VERSION" ] ||
	fail "build changed the working tree; version is now $DETECTED_VERSION"

printf 'Pushing tag %s to %s...\n' "$RELEASE_VERSION" "$REMOTE"
git -C "$PROJECT_DIR" push "$REMOTE" "refs/tags/$RELEASE_VERSION:refs/tags/$RELEASE_VERSION"
TAG_PUSHED=1
trap - EXIT HUP INT TERM

printf 'Released %s from branch %s\n' "$RELEASE_VERSION" "$BRANCH"
printf 'Distribution files: %s/dist\n' "$PROJECT_DIR"
