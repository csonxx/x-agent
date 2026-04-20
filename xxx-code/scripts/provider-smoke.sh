#!/usr/bin/env bash

set -euo pipefail

provider="${1:?provider is required}"
model="${2:?model is required}"
prompt="${3:-Reply with exact text PONG and nothing else.}"
expected="${EXPECTED_TEXT:-PONG}"

cmd=(
  go run ./cmd/xxx-code
  --provider "${provider}"
  --model "${model}"
  --max-turns 1
  --max-tokens 64
  --stream=false
  --print "${prompt}"
)

if [[ -n "${BASE_URL_OVERRIDE:-}" ]]; then
  cmd+=(--base-url "${BASE_URL_OVERRIDE}")
fi

if [[ -n "${API_VERSION_OVERRIDE:-}" ]]; then
  cmd+=(--anthropic-version "${API_VERSION_OVERRIDE}")
fi

output="$("${cmd[@]}")"
printf '%s\n' "${output}"

trimmed="$(printf '%s' "${output}" | tr -d '\r' | sed 's/[[:space:]]\+$//')"
if [[ "${trimmed}" != "${expected}" ]]; then
  echo "unexpected provider smoke output for ${provider}: ${trimmed}" >&2
  exit 1
fi

echo "provider smoke passed for ${provider} (${model})"
