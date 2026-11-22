#!/usr/bin/env bash
# Prints the version of the checked-out code.
#
#   0.1.N              on the default branch: N counts the commits on its
#                      first-parent line, so every merged pull request adds one
#                      (in the public repository: from its v0.1.K tags)
#   0.1.N-dev.<sha>    elsewhere (a branch or pull request): N is the number
#                      the code gets once merged
#   ...-dirty          with uncommitted changes
#
# MAIN_REF names the default branch (default origin/HEAD, else origin/main);
# RELEASE=1 forces the release form (CI on a push to the default branch).
# Needs the full history (actions/checkout with fetch-depth: 0).
# installer/build.ps1 has the same rules for local Windows builds.
set -euo pipefail

main="${MAIN_REF:-origin/HEAD}"
if ! git rev-parse -q --verify "$main" >/dev/null; then
	main=origin/main
fi
sha=$(git rev-parse --short HEAD)

# count gives N for a commit: its first-parent commits, or, in the public
# repository (scripts/publish.sh), the nearest v0.1.K tag plus the commits
# after it, since that history starts at K
count() {
	local tag
	if tag=$(git describe --tags --match 'v0.1.*' --abbrev=0 --first-parent "$1" 2>/dev/null) &&
		[[ ${tag#v0.1.} =~ ^[0-9]+$ ]]; then
		echo $((${tag#v0.1.} + $(git rev-list --count --first-parent "$tag..$1")))
	else
		git rev-list --count --first-parent "$1"
	fi
}

if [ "${RELEASE:-}" = 1 ] || [ "$(git rev-parse HEAD)" = "$(git rev-parse "$main")" ]; then
	v="0.1.$(count HEAD)"
else
	base=$(git merge-base HEAD "$main")
	v="0.1.$(($(count "$base") + 1))-dev.$sha"
fi
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
	v="$v-dirty"
fi
echo "$v"
