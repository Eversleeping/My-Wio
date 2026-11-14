#!/usr/bin/env bash
# Usage: check-agent-version.sh [BASE [HEAD]]
# With HEAD, check exactly that committed range. Without HEAD, include the worktree
# and non-ignored untracked Go files. BASE defaults to the upstream merge-base,
# then HEAD's parent (or the empty tree for an initial commit).
# A single clean commit may opt out with: [skip-agent-version]: explanation
# The exemption cannot hide additional uncommitted changes or a multi-commit range.
set -euo pipefail
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
head_ref=${2:-HEAD}
head_commit=$(git rev-parse --verify "$head_ref^{commit}")
empty_tree=$(git hash-object -w -t tree /dev/null)
if [[ $# -ge 1 ]]; then
  base_ref=${1:-$empty_tree}
  [[ ! $base_ref =~ ^0+$ ]] || base_ref=$empty_tree
else
  base_ref=$(git merge-base "$head_commit" '@{upstream}' 2>/dev/null || git rev-parse --verify "$head_commit^" 2>/dev/null || printf '%s' "$empty_tree")
fi
git cat-file -e "$base_ref^{tree}" 2>/dev/null || { echo "Invalid base revision: $base_ref" >&2; exit 1; }
include_worktree=true
[[ $# -lt 2 ]] || include_worktree=false

dependency_dirs=$(go list -deps -f '{{if and .Module .Module.Main}}{{.Dir}}{{end}}' ./cmd/agent) || { echo 'Cannot determine Agent dependencies' >&2; exit 1; }
declare -A agent_dirs=()
while IFS= read -r directory; do
  [[ -z $directory ]] && continue
  if [[ $directory == "$repo_root" ]]; then
    agent_dirs[.]=1
  elif [[ $directory == "$repo_root/"* ]]; then
    agent_dirs["${directory#"$repo_root/"}"]=1
  fi
done <<< "$dependency_dirs"
[[ ${#agent_dirs[@]} -gt 0 ]] || { echo 'Cannot determine Agent dependencies' >&2; exit 1; }

changed_files=()
while IFS= read -r -d '' file; do changed_files+=("$file"); done < <(
  if $include_worktree; then
    git diff --name-only --no-renames -z "$base_ref" --
    git ls-files --others --exclude-standard -z
  else
    git diff --name-only --no-renames -z "$base_ref" "$head_commit" --
  fi
)
affected=()
for file in "${changed_files[@]}"; do
  [[ $file == *.go && $file != *_test.go ]] || continue
  directory=${file%/*}
  [[ $directory != "$file" ]] || directory=.
  [[ ${agent_dirs[$directory]:-} == 1 ]] && affected+=("$file")
done
if [[ ${#affected[@]} == 0 ]]; then
  echo 'Agent version check: no Agent source changes'
  exit 0
fi

version_pattern='s/^[[:space:]]*var Version = "([^"]+)"[[:space:]]*$/\1/p'
base_version=$(git show "$base_ref:internal/buildinfo/version.go" 2>/dev/null | sed -nE "$version_pattern" || true)
if $include_worktree; then
  head_version=$(sed -nE "$version_pattern" internal/buildinfo/version.go)
else
  head_version=$(git show "$head_commit:internal/buildinfo/version.go" | sed -nE "$version_pattern")
fi
version_is_newer() {
  [[ $head_version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  [[ -n $base_version ]] || return 0
  [[ $base_version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  local -a old_parts new_parts
  local index old_value new_value
  IFS=. read -r -a old_parts <<< "$base_version"
  IFS=. read -r -a new_parts <<< "$head_version"
  for index in 0 1 2; do
    old_value=$((10#${old_parts[index]}))
    new_value=$((10#${new_parts[index]}))
    (( new_value <= old_value )) || return 0
    (( new_value >= old_value )) || return 1
  done
  return 1
}
if version_is_newer; then
  printf 'Agent version check: %s -> %s\n' "${base_version:-initial}" "$head_version"
  exit 0
fi

if [[ $head_version == "$base_version" ]] &&
   [[ $base_ref != "$empty_tree" ]] &&
   [[ $(git rev-list --count "$base_ref..$head_commit") == 1 ]] &&
   git diff --quiet "$head_commit" -- &&
   [[ -z $(git ls-files --others --exclude-standard) ]] &&
   git log -1 --format=%B "$head_commit" | grep -Eq '\[skip-agent-version\]:[[:space:]]+[^[:space:]]'; then
  echo 'Agent version check: explicit exemption for this single clean commit'
  exit 0
fi
printf 'Agent source changed without a version increase (%s -> %s):\n' "${base_version:-missing}" "${head_version:-missing}" >&2
printf '  %s\n' "${affected[@]}" >&2
exit 1
