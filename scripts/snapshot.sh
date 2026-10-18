#!/usr/bin/env bash
# Export a clean snapshot of Panurus at one commit, so the index and the eval
# set always refer to exactly the same code (PRD section 11, "Pinned commit").
#
#   scripts/snapshot.sh <panurus git dir> <commit-ish> [dest]
#
# Uses git archive, so untracked and ignored files in the working copy are
# never indexed, and the working copy itself is not touched.
set -euo pipefail

src=${1:?usage: snapshot.sh <panurus git dir> <commit-ish> [dest]}
rev=${2:?usage: snapshot.sh <panurus git dir> <commit-ish> [dest]}
sha=$(git -C "$src" rev-parse --verify "$rev^{commit}")
dest=${3:-.data/panurus@${sha:0:12}}

if [[ -e "$dest" ]]; then
	echo "$dest already exists" >&2
	exit 1
fi
mkdir -p "$dest"
git -C "$src" archive --format=tar "$sha" | tar -x -C "$dest"
echo "$sha" > "$dest/.ask-panurus-commit"
echo "$dest"
