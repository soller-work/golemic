#!/usr/bin/env sh
# E2E guard: block modification or deletion of existing tracked test/e2e/ files
# without explicit user approval. Adding new files is always allowed.
#
# Decision table (evaluated in order):
#   1. No modified/deleted test/e2e files → exit 0 silently.
#   2. E2E_GUARD_ALLOW=1 → exit 0 with a notice (human override; agents must not set this).
#   3. GOLEMIC_ISSUE_BODY_FILE readable and contains "E2E-MODIFY-APPROVED" → exit 0 with notice.
#   4. Otherwise → exit 1 with the list of offending files and remediation instructions.
set -e

LINT_BASE_REF="${LINT_BASE_REF:-origin/main}"

# Compute merge base; fall back to LINT_BASE_REF on shallow clones.
if ! BASE=$(git merge-base HEAD "$LINT_BASE_REF" 2>/dev/null); then
  printf 'lint-e2e-guard: merge-base with %s unavailable; comparing directly\n' "$LINT_BASE_REF" >&2
  BASE="$LINT_BASE_REF"
fi

# Collect modified/deleted/renamed files under test/e2e/ from three sources so
# that both committed and uncommitted changes are caught.
COMMITTED=$(git diff --name-status "$BASE" HEAD -- 'test/e2e/' 2>/dev/null \
  | awk '$1 ~ /^[MDR]/ { print $NF }')
STAGED=$(git diff --cached --name-status "$BASE" -- 'test/e2e/' 2>/dev/null \
  | awk '$1 ~ /^[MDR]/ { print $NF }')
UNSTAGED=$(git diff --name-status "$BASE" -- 'test/e2e/' 2>/dev/null \
  | awk '$1 ~ /^[MDR]/ { print $NF }')

OFFENDING=$(printf '%s\n%s\n%s\n' "$COMMITTED" "$STAGED" "$UNSTAGED" \
  | grep -v '^[[:space:]]*$' | sort -u)

if [ -z "$OFFENDING" ]; then
  exit 0
fi

if [ "${E2E_GUARD_ALLOW:-}" = "1" ]; then
  printf 'lint-e2e-guard: E2E_GUARD_ALLOW=1 override; ignoring modifications to:\n' >&2
  printf '%s\n' "$OFFENDING" | sed 's/^/  /' >&2
  exit 0
fi

if [ -n "${GOLEMIC_ISSUE_BODY_FILE:-}" ]; then
  if [ ! -r "$GOLEMIC_ISSUE_BODY_FILE" ]; then
    printf 'lint-e2e-guard: GOLEMIC_ISSUE_BODY_FILE=%s is not readable; treating as no approval\n' \
      "$GOLEMIC_ISSUE_BODY_FILE" >&2
  elif grep -qE '^[[:space:]]*E2E-MODIFY-APPROVED[[:space:]]*$' "$GOLEMIC_ISSUE_BODY_FILE"; then
    printf 'lint-e2e-guard: E2E-MODIFY-APPROVED found in issue body; allowing modification of:\n' >&2
    printf '%s\n' "$OFFENDING" | sed 's/^/  /' >&2
    exit 0
  fi
fi

printf 'lint-e2e-guard: existing E2E tests may only be modified with explicit user permission;\n' >&2
printf '  the driving issue must contain the line E2E-MODIFY-APPROVED\n' >&2
printf '  (granted by the user in the grill session)\n' >&2
printf 'Offending files:\n' >&2
printf '%s\n' "$OFFENDING" | sed 's/^/  /' >&2
exit 1
