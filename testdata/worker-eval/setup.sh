#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Seed the evaluation repository or add one run's worktree.
#   setup.sh seed <dir>
#   setup.sh run <seed-dir> <worktree-dir> <branch>
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

case "${1:-}" in
seed)
	dir=$2
	mkdir -p "$dir"
	cp -R "$here/fixture/." "$dir"/
	git -C "$dir" init -q --initial-branch=main
	git -C "$dir" config user.name "Worker Evaluation"
	git -C "$dir" config user.email "worker-eval@example.invalid"
	git -C "$dir" add -A
	GIT_AUTHOR_DATE=2026-10-23T00:00:00Z GIT_COMMITTER_DATE=2026-10-23T00:00:00Z \
		git -C "$dir" commit -q -m "fixture: seed"
	git -C "$dir" rev-parse HEAD
	;;
run)
	seed=$2
	worktree=$3
	branch=$4
	git -C "$seed" worktree add -q -b "$branch" "$worktree" HEAD
	git -C "$worktree" config user.name "Worker Evaluation"
	git -C "$worktree" config user.email "worker-eval@example.invalid"
	;;
*)
	echo "usage: setup.sh seed <dir> | setup.sh run <seed> <worktree> <branch>" >&2
	exit 2
	;;
esac
