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

## opencode (custom provider) — VERIFIED END-TO-END ✅

Tested live with opencode 1.18.21 on Windows: a full `opencode run` turn
completed through outfix-proxy against a custom provider.

Three things must be right (miss one and you get misleading errors like
`No active credentials for provider: openai`):

1. **Provider config** (`opencode.json`, project-level):

   ```json
   {
     "$schema": "https://opencode.ai/config.json",
     "model": "outfix/<upstream-model-id>",
     "provider": {
       "outfix": {
         "npm": "@ai-sdk/openai-compatible",
         "name": "Outfix Cleaned",
         "options": {
           "baseURL": "http://127.0.0.1:8643/v1",
           "apiKey": "any-non-empty-string"
         },
         "models": {
           "<upstream-model-id>": {
             "name": "DeepSeek V4 Flash (outfix-cleaned)",
             "modalities": { "input": ["text"], "output": ["text"] },
             "tool_call": true
           }
         }
       }
     }
   }
   ```

   The `models` keys must match the IDs your upstream serves (`GET /v1/models`
   through the proxy).

2. **auth.json entry keyed by the provider id.** opencode checks its own
   credential store *before* trusting `options.apiKey` for npm providers:

   ```
   # Windows: %USERPROFILE%\.local\share\opencode\auth.json
   # Linux/macOS: ~/.local/share/opencode/auth.json
   { "outfix": { "type": "api", "key": "any-non-empty-string" } }
   ```

   Note: on Windows, `XDG_DATA_HOME` is ignored for this file — always use the
   profile path above.

3. **Provide an explicit session title** when scripting headless runs
   (`opencode run --title x ...`) so the built-in title generator does not
   call a different provider.

Verified behavior matrix from that live test (updated after the streaming
cleaner landed in v0.7.0):

| Path | Result |
|---|---|
| Non-streaming request → proxy → upstream | response body cleaned ✅ (curl-proven side-by-side) |
| Streaming request (`stream:true`) → proxy | buffered & flushed clean at `finish_reason` ✅ — live-verified: opencode rendered `{"active": true, "n": null}` from a polluted SSE stream, audit log showed 5 repairs |
| Proxy down / wrong port | opencode surfaces `Bad Gateway: read upstream` from outfix-proxy (causality proven) |

Streaming design note: deltas are accumulated; cleaning runs once on the full
payload when `finish_reason` arrives, then a single synthetic content delta +
terminators are emitted. Streams exceeding `-max-stream-buffer` bytes degrade
to pass-through mid-stream (memory bound). Use `-stream-mode pass` for
verbatim piping.

### Original reference config

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
the *assistant's* text before display/history.

**Better path (v0.8.0+): outfix-proxy now speaks the Anthropic Messages
format too.** Point Claude Code at the proxy and every assistant text block
is cleaned at the source:

```bash
outfix-proxy -listen :8643 -upstream https://your-anthropic-compatible-upstream -model deepseek
export ANTHROPIC_BASE_URL=http://127.0.0.1:8643
claude
```

- Non-streaming `/v1/messages`: `content[].text` blocks cleaned in place;
  `tool_use` / other non-text blocks preserved byte-for-byte (map-preserving
  re-marshal).
- Streaming `/v1/messages` SSE: event order and count preserved exactly;
  accumulated `text_delta` content is cleaned and injected into the first
  delta, subsequent deltas are emptied (`content_block_stop`,
  `message_delta`, `message_stop` all intact).

Wire-format verified with mock upstreams covering both shapes plus
tool_use preservation; full-session proof requires the `claude` CLI, which
was not available on the test machine — treat that last hop accordingly.

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
| Streaming SSE cleaning | ✅ implemented (buffered flush at finish; `-stream-mode pass` to opt out) |
| Anthropic Messages API format | ✅ implemented v0.8.0 (JSON + SSE, tool_use preserved) |
| Rewriting assistant text inside Claude Code sessions | solved at gateway level via ANTHROPIC_BASE_URL → outfix-proxy |
| Streamed deltas beyond -max-stream-buffer degrade to pass-through | memory bound by design |
