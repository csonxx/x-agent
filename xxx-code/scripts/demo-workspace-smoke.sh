#!/usr/bin/env bash

set -euo pipefail

output_dir="${1:-.artifacts/demo-workspace-smoke}"
log_file="${output_dir}/go-test.jsonl"
summary_file="${output_dir}/summary.txt"

mkdir -p "${output_dir}"
: >"${log_file}"
: >"${summary_file}"

run_check() {
  local label="$1"
  local pkg="$2"
  local test_name="$3"
  local started_at ended_at duration

  started_at="$(date +%s)"
  echo "[demo-workspace-smoke] running ${label} (${pkg} ${test_name})"
  go test -count=1 -run "^${test_name}$" -json "${pkg}" | tee -a "${log_file}"
  ended_at="$(date +%s)"
  duration="$((ended_at - started_at))"

  {
    echo "${label}: passed"
    echo "package: ${pkg}"
    echo "test: ${test_name}"
    echo "duration_seconds: ${duration}"
    echo
  } >>"${summary_file}"
}

run_check "config" "./internal/config" "TestDemoWorkspaceConfigIsLoadable"
run_check "plugin" "./internal/plugins" "TestDemoWorkspacePluginLoadsAndRuns"
run_check "mcp" "./internal/mcp" "TestDemoWorkspaceMCPServerStartsAndLoadsTools"
run_check "integration" "./internal/integration" "TestUserStoryDemoWorkspaceCanRunAFullExtensionTurn"

echo "demo workspace smoke passed"
cat "${summary_file}"
