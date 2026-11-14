#!/usr/bin/env bash
# Isolated repositories exercise the guard without touching this checkout.
set -euo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
for variable in ${!GIT_CONFIG@}; do unset "$variable"; done
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GOWORK=off
fixture_root=$(mktemp -d)
trap 'rm -rf "$fixture_root"' EXIT
case_number=0
new_fixture() {
  case_number=$((case_number+1))
  mkdir -p "$fixture_root/$case_number"
  cd "$fixture_root/$case_number"
  git init -q
  git config user.name 'Version guard test'
  git config user.email 'version-guard@example.invalid'
  mkdir -p cmd/agent internal/{agent,protocol,buildinfo,httpapi}
  printf 'module example.org/guard\n\ngo 1.22\n' > go.mod
  printf 'package main\nimport _ "example.org/guard/internal/agent"\nfunc main() {}\n' > cmd/agent/main.go
  printf 'package agent\nimport (_ "example.org/guard/internal/buildinfo"; _ "example.org/guard/internal/protocol")\n' > internal/agent/agent.go
  printf 'package protocol\n' > internal/protocol/protocol.go
  printf 'package httpapi\n' > internal/httpapi/api.go
  printf 'package buildinfo\nvar Version = "0.3.16"\n' > internal/buildinfo/version.go
  git add .
  git commit -qm 'fixture baseline'
  fixture_base=$(git rev-parse HEAD)
}
check() {
  local expected=$1 label=$2 result=0
  shift 2
  "$script_dir/check-agent-version.sh" "$@" > "$fixture_root/check.log" 2>&1 || result=$?
  if [[ $result != "$expected" ]]; then
    cat "$fixture_root/check.log"
    printf 'FAIL: %s (expected %s, got %s)\n' "$label" "$expected" "$result" >&2
    exit 1
  fi
  printf 'PASS: %s\n' "$label"
}
commit_changes() { git add .; git commit -qm "$1"; }
new_fixture
printf '\n// API only\n' >> internal/httpapi/api.go
commit_changes 'HTTP change'
check 0 'HTTP-only commit' "$fixture_base" HEAD
new_fixture
printf '\n// Agent change\n' >> internal/agent/agent.go
commit_changes 'Agent change'
check 1 'Agent commit without version bump' "$fixture_base" HEAD
new_fixture
printf '\n// Agent change\n' >> internal/agent/agent.go
printf 'package buildinfo\nvar Version = "0.3.17"\n' > internal/buildinfo/version.go
commit_changes 'Agent and version'
check 0 'Agent commit with version bump' "$fixture_base" HEAD
new_fixture
printf '\n// Shared protocol change\n' >> internal/protocol/protocol.go
check 1 'uncommitted shared protocol' "$fixture_base"
new_fixture
printf 'package agent\n' > internal/agent/new.go
check 1 'untracked Agent file' "$fixture_base"
new_fixture
printf 'package agent\n' > internal/agent/guard_test.go
printf 'documentation\n' > README.md
commit_changes 'Tests and docs'
check 0 'tests and documentation only' "$fixture_base" HEAD
new_fixture
printf '\n// Agent change\n' >> internal/agent/agent.go
printf 'package buildinfo\nvar Version = "0.3.15"\n' > internal/buildinfo/version.go
commit_changes 'Downgrade'
check 1 'version downgrade' "$fixture_base" HEAD
new_fixture
printf '\n// Comment only\n' >> internal/agent/agent.go
commit_changes '[skip-agent-version]: comment-only change'
check 0 'single clean explicit exemption' "$fixture_base" HEAD
printf '\n// Additional change\n' >> internal/agent/agent.go
check 1 'exemption cannot hide workspace changes' "$fixture_base"
commit_changes '[skip-agent-version]: another comment'
check 1 'exemption cannot hide multiple commits' "$fixture_base" HEAD
new_fixture
printf 'package agent\n' > internal/agent/deleted.go
commit_changes 'Add file'
fixture_base=$(git rev-parse HEAD)
rm internal/agent/deleted.go
commit_changes 'Delete Agent file'
check 1 'deleted Agent source' "$fixture_base" HEAD
new_fixture
printf '\n// Invalid old revision must fail\n' >> internal/agent/agent.go
check 1 'invalid base is rejected' missing-base HEAD
new_fixture
printf '\n// CLI entry point\n' >> cmd/agent/main.go
check 1 'Agent entry point' "$fixture_base"
new_fixture
printf 'package buildinfo\nvar Version = "0.3.99"\n' > internal/buildinfo/version.go
commit_changes 'Version in committed range'
check 0 'explicit committed range with version increase' "$fixture_base" HEAD
printf 'package buildinfo\nvar Version = "0.3.16"\n' > internal/buildinfo/version.go
printf '\n// Uncommitted Agent change\n' >> internal/agent/agent.go
check 1 'worktree mode reads the actual worktree version' "$fixture_base"
new_fixture
check 0 'new branch with zero base' 0000000000000000000000000000000000000000 HEAD
new_fixture
printf '\nimport _ "example.invalid/missing"\n' >> internal/agent/agent.go
check 1 'dependency discovery errors fail closed' "$fixture_base"
new_fixture
printf 'package agent\n' > 'internal/agent/source with spaces.go'
check 1 'untracked filename with spaces' "$fixture_base"
new_fixture
printf 'internal/agent/ignored.go\n' > .gitignore
printf 'package agent\n' > internal/agent/ignored.go
check 0 'ignored files are excluded' "$fixture_base"
new_fixture
printf 'package buildinfo\nvar Version = "0.3.15"\n' > internal/buildinfo/version.go
commit_changes '[skip-agent-version]: cannot exempt a downgrade'
check 1 'exemption cannot allow version downgrade' "$fixture_base" HEAD
printf 'All Agent version guard cases passed.\n'  
