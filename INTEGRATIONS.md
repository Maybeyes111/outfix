# Harness integrations (verified against official docs)

All recipes below were checked against each project's documentation in
August 2026. Where behavior is inferred rather than documented, it is called
out explicitly.

---

## opencode (custom provider)

opencode documents custom OpenAI-compatible providers via
`provider.<id>` with `"npm": "@ai-sdk/openai-compatible"` and an
`options.baseURL` override. That means outfix-proxy drops in directly.

1. Run the proxy next to your router/provider:

   ```bash
   outfix-proxy -listen :8643 -upstream http://localhost:20128/v1 -model deepseek
   ```

2. Point a custom provider at it (`opencode.json`):

   ```json
   {
     "$schema": "https://opencode.ai/config.json",
     "provider": {
       "outfix": {
         "npm": "@ai-sdk/openai-compatible",
         "name": "My Models (outfix-cleaned)",
         "options": { "baseURL": "http://127.0.0.1:8643/v1" },
         "models": {
           "deepseek-v4-flash": { "name": "DeepSeek V4 Flash (cleaned)" }
         }
       }
     }
   }
   ```

3. `/models` and pick the cleaned model. Every assistant message is cleaned
   before opencode parses tool calls or stores the turn.

---

## Claude Code (hooks)

Claude Code exposes lifecycle hooks configured in `.claude/settings.json`
(committed per-project) or `~/.claude/settings.json`. Verified events:
`PostToolUse` fires after every successful tool call and delivers the JSON
input (including `tool_input` / `tool_response`) to your handler on stdin.

outfix's role here is the **conservative** one from our multi-turn rules:
audit tool results for pollution and surface context — never silently rewrite
data you did not produce.

`.claude/hooks/outfix-posttooluse.sh`:

```bash
#!/bin/bash
# Requires: jq, outfix on PATH. Exit 0 always; this hook only advises.
INPUT=$(cat)
RESP=$(printf '%s' "$INPUT" | jq -r '.tool_response // empty' 2>/dev/null)

[ -z "$RESP" ] && exit 0

CLEAN=$(printf '%s' "$RESP" | outfix -format plain 2>/dev/null)

if [ -n "$CLEAN" ] && [ "$CLEAN" != "$RESP" ]; then
  jq -n --arg note "outfix detected malformed/polluted formatting in the \
last tool response (whitespace, escapes or wrapper residue). Re-read it \
carefully before quoting it verbatim." '{
    hookSpecificOutput: {
      hookEventName: "PostToolUse",
      additionalContext: $note
    }
  }'
fi
exit 0
```

`.claude/settings.json`:

```json
{
  "hooks": {
    "PostToolUse": [
      { "matcher": "*", "hooks": [ { "type": "command",
        "command": "${CLAUDE_PROJECT_DIR}/.claude/hooks/outfix-posttooluse.sh" } ] }
    ]
  }
}
```

Caveat (honest): Claude Code does not expose a supported hook that rewrites
the *assistant's* text before display/history. For full assistant-side
cleaning with Claude Code, route it through an OpenAI-compatible gateway
(like the user's 9router) plus outfix-proxy, or wait for the planned
Anthropic-format support in outfix-proxy.

---

## Hermes Agent (provider swap)

Verified pattern from Hermes documentation/config: providers take a
`base_url`. Swap it for the proxy so Telegram/Discord gateway replies are
cleaned at the source:

```yaml
# ~/.hermes/config.yaml
providers:
  myrouter:
    base_url: http://127.0.0.1:8643/v1
    api_key_env: ROUTER_API_KEY
```

Optional follow-up: package an outfix *skill* (`~/.hermes/skills/`) so the
agent can invoke `outfix` deliberately on pasted text. Not built yet — PRs
welcome.

---

## Known limitations across all harnesses

| Limitation | Status |
|---|---|
| Streaming (SSE) responses pass through uncleaned | documented; chunk-aware cleaner is future work |
| Anthropic Messages API format in outfix-proxy | planned |
| Rewriting assistant text inside Claude Code sessions | not exposed by upstream; use gateway-level cleaning instead |
