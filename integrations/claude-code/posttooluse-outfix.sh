#!/bin/bash
# Claude Code PostToolUse hook — audit tool responses with outfix.
# Conservative by design: never rewrites the response, only advises context.
# Requires jq + outfix on PATH. Always exits 0.

INPUT=$(cat)
RESP=$(printf '%s' "$INPUT" | jq -r '.tool_response // empty' 2>/dev/null)

[ -z "$RESP" ] && exit 0

CLEAN=$(printf '%s' "$RESP" | outfix -format plain 2>/dev/null)

if [ -n "$CLEAN" ] && [ "$CLEAN" != "$RESP" ]; then
  jq -n --arg note "outfix detected malformed/polluted formatting in the last tool response (whitespace, escapes or wrapper residue). Re-read it carefully before quoting it verbatim." '{
    hookSpecificOutput: {
      hookEventName: "PostToolUse",
      additionalContext: $note
    }
  }'
fi
exit 0
