#!/usr/bin/env bash
# Publishes the default branch to the public repository
# (github.com/bytebanger-nl/HomeEnergyWatch). Work happens in the private
# repository; this script copies it over without its history:
#
#   - every commit on the default branch's first-parent line (one per merged
#     pull request) becomes one public commit with the same files, so builds
#     from both repositories get the same version (scripts/version.sh)
#   - the message is the pull request's title, the author is PUBLISH_NAME
#     <PUBLISH_EMAIL>, the date is the original one; nothing refers back to
#     the private commits
#   - each public commit is tagged v0.1.N, which scripts/version.sh and
#     installer/build.ps1 use as the starting count
#
#   scripts/publish.sh [-n]     # -n: build the commits, show them, push nothing
#
# The first run publishes the current state as one commit. After that the
# public main must be what this script last pushed: commits made directly in
# the public repository stop it (bring such changes into the private
# repository instead). PUBLISH_REMOTE (default "public") is the remote of the
# public repository, PUBLISH_SOURCE (default origin/main) what is published.
set -euo pipefail
cd "$(dirname "$0")/.."

dry=
if [ "${1:-}" = -n ]; then dry=1; fi
remote=${PUBLISH_REMOTE:-public}
source=${PUBLISH_SOURCE:-origin/main}
name=${PUBLISH_NAME:-bytebanger-nl}
email=${PUBLISH_EMAIL:-noreply@bytebanger.nl}

if ! git remote get-url "$remote" >/dev/null 2>&1; then
	echo "no remote \"$remote\"; add it with:" >&2
	echo "  git remote add $remote https://github.com/bytebanger-nl/HomeEnergyWatch.git" >&2
	exit 1
fi
if [ "$source" = origin/main ]; then git fetch -q origin; fi
# the public refs go to their own namespace, so its tags never mix with ours
git for-each-ref --format='%(refname)' refs/publish/ | while read -r ref; do git update-ref -d "$ref"; done
if [ -n "$(git ls-remote --heads "$remote" main)" ]; then
	git fetch -q --no-tags "$remote" '+refs/heads/main:refs/publish/main' '+refs/tags/v0.1.*:refs/publish/tags/v0.1.*'
fi

mapfile -t commits < <(git rev-list --first-parent --reverse "$source")
total=${#commits[@]}

# title gives the public message of a private commit: the pull request's
# title for a merge, else its subject; the old account name is replaced
title() {
	local subject body
	subject=$(git log -1 --format=%s "$1")
	if [[ $subject =~ ^Merge\ pull\ request\ \#[0-9]+\ from\  ]]; then
		body=$(git log -1 --format=%b "$1" | sed '/^[[:space:]]*$/d' | head -1)
		if [ -n "$body" ]; then subject=$body; fi
	fi
	echo "${subject//hawkman67/$name}"
}

# commit makes a public commit with the files and dates of private commit $1
commit() {
	local c=$1 msg=$2 parent=${3:-}
	GIT_AUTHOR_NAME=$name GIT_AUTHOR_EMAIL=$email GIT_AUTHOR_DATE=$(git log -1 --format=%aI "$c") \
		GIT_COMMITTER_NAME=$name GIT_COMMITTER_EMAIL=$email GIT_COMMITTER_DATE=$(git log -1 --format=%cI "$c") \
		git commit-tree "$c^{tree}" ${parent:+-p "$parent"} -m "$msg"
}

refs=()
if tip=$(git rev-parse -q --verify refs/publish/main); then
	tag=$(git for-each-ref --points-at "$tip" --format='%(refname:strip=3)' 'refs/publish/tags/v0.1.*' | head -1)
	done_count=${tag#v0.1.}
	if ! [[ $done_count =~ ^[0-9]+$ ]]; then
		echo "the public main ($(git rev-parse --short "$tip")) has no v0.1.N tag: it was changed outside this script" >&2
		exit 1
	fi
	if [ "$done_count" -gt "$total" ] || [ "$(git rev-parse "$tip^{tree}")" != "$(git rev-parse "${commits[done_count - 1]}^{tree}")" ]; then
		echo "the public main ($tag) does not match commit $done_count of $source: it was changed outside this script" >&2
		exit 1
	fi
	if [ "$done_count" -eq "$total" ]; then
		echo "nothing to publish: the public main is at $tag"
		exit 0
	fi
	for ((i = done_count + 1; i <= total; i++)); do
		tip=$(commit "${commits[i - 1]}" "$(title "${commits[i - 1]}")" "$tip")
		refs+=("$tip:refs/tags/v0.1.$i")
	done
	from=$((done_count + 1))
else
	tip=$(commit "${commits[total - 1]}" "Home Energy Watch 0.1.$total")
	refs+=("$tip:refs/tags/v0.1.$total")
	from=$total
fi

git log --format='%h %ad %s' --date=short -n $((total - from + 1)) "$tip"
if [ -n "$dry" ]; then
	echo "dry run: would push 0.1.$from to 0.1.$total to $remote"
	exit 0
fi
git push --atomic "$remote" "$tip:refs/heads/main" "${refs[@]}"
echo "published 0.1.$from to 0.1.$total"
