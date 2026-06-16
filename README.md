# mini

A small, provider-agnostic terminal AI agent written in Go.

- Single static binary (~6 MB, `CGO_ENABLED=0`).
- Pick your model: Gemini (OAuth, free tier), OpenAI, or Anthropic.
- Tools the agent actually needs: `bash`, `write` (anchor-based edits),
  `read` (line-numbered), `glob`, `task` (sub-agent), `memory`, plus
  `web_search` / `web_fetch`, background jobs (`bash_input` /
  `bash_output` / `bash_kill`), and MCP servers over stdio or HTTP.
- Risk-based approval prompts for destructive shell commands. `--yolo`
  to skip.
- Automatic history compaction when the prompt grows past a configurable
  threshold (a chat doesn't die slowly to context-window 429s).

---

## Install

```bash
go install github.com/arcaela/mini-cli@latest
```

Or grab a prebuilt binary from the [Releases page](../../releases) once a
tag has been pushed (CI workflow ships linux/amd64, linux/arm64,
darwin/arm64).

---

## First run

The default provider is Gemini, using the [Gemini Code Assist for
individuals](https://codeassist.google.com/) free tier. Authenticate
once:

```bash
mini provider gemini auth
# open the URL, click Allow, paste the redirect URL back:
mini provider gemini auth-complete "<paste>"
```

Then:

```bash
mini                                  # interactive REPL
mini chat "list every .go file in cmd/ and tell me what main.go does"
```

Want OpenAI or Anthropic instead?

```bash
export OPENAI_API_KEY=sk-...
mini chat -p openai -m gpt-5 "..."

export ANTHROPIC_API_KEY=sk-ant-...
mini chat -p anthropic "..."
```

---

## Slash commands inside the REPL

| Command               | What it does                                                    |
| --------------------- | --------------------------------------------------------------- |
| `/help`               | show available commands                                         |
| `/tools`              | list registered tools (built-in + MCP)                          |
| `/jobs`               | list background jobs started by `bash_input`                    |
| `/tokens`             | last-turn + cumulative usage; compaction headroom               |
| `/compact`            | force history compaction now                                    |
| `/save [name]`        | persist this session to `~/.mini/sessions/<id>.jsonl`           |
| `/load <id>`          | replace history with a saved session                            |
| `/sessions`           | list saved sessions                                             |
| `/yolo [on\|off]`     | toggle the approval prompt for risky tool calls                 |
| `/model [<name>]`     | show or change model                                            |
| `/whoami`             | provider account info                                           |
| `/clear`              | reset the conversation history                                  |
| `/quit`               | exit                                                            |

---

## Configuration

All neutral knobs are environment variables (or per-run flags on
`mini chat`):

| Variable                | Meaning                                                   | Default               |
| ----------------------- | --------------------------------------------------------- | --------------------- |
| `MINI_PROVIDER`         | which LLM backend                                         | `gemini`              |
| `MINI_MODEL`            | model name                                                | from provider         |
| `MINI_MAX_STEPS`        | agent loop step cap per turn                              | `25`                  |
| `MINI_MAX_PARALLEL`     | concurrent tool executions                                | `4`                   |
| `MINI_COMPACT_THRESHOLD`| prompt-token threshold that triggers compaction           | `8000`                |
| `MINI_SYSTEM_PROMPT`    | replace the default system prompt (memory still appended) | unset                 |
| `MINI_YOLO=1`           | skip approval prompts                                     | unset                 |
| `MINI_QUIET=1`          | suppress retry/cleanup log lines on stderr                | unset                 |

Provider-specific:

| Variable                  | Meaning                                                |
| ------------------------- | ------------------------------------------------------ |
| `OPENAI_API_KEY`          | required for `--provider openai`                       |
| `OPENAI_BASE_URL`         | for OpenAI-compatible endpoints (Groq, Together, etc.) |
| `ANTHROPIC_API_KEY`       | required for `--provider anthropic`                    |
| `ANTHROPIC_BASE_URL`      | for self-hosted proxies                                |
| `GEMINI_RETRY_MAX`        | how many times to retry on 429 / 5xx                   |
| `GEMINI_RETRY_MAX_WAIT`   | cap (seconds) on a single retry backoff                |

Path overrides (mostly for tests):

| Variable                | Default                          |
| ----------------------- | -------------------------------- |
| `MINI_JOBS_DIR`         | `~/.mini/jobs/`                  |
| `MINI_MEMORY_DIR`       | `~/.mini/memory/`                |
| `MINI_SESSIONS_DIR`     | `~/.mini/sessions/`              |
| `MINI_MCP_CONFIG`       | `~/.mini/mcp.json`               |

---

## Adding your own data

**Memory** (auto-loaded into every future system prompt; survives sessions
and is inherited by sub-agents):

```bash
mkdir -p ~/.mini/memory
echo "I work in Go and prefer responses in Spanish." > ~/.mini/memory/user.md
```

Inside the REPL the agent can write to memory itself:

```
> remember that my main project is mini-cli
```

**MCP servers** (extend the tool surface without code changes). Drop a
config at `~/.mini/mcp.json`:

```json
{
  "servers": {
    "filesystem": {
      "command": "npx",
      "args": ["@modelcontextprotocol/server-filesystem", "/home/me"]
    },
    "remote-tool": {
      "url": "https://example.com/mcp",
      "headers": {"Authorization": "Bearer ..."}
    }
  }
}
```

Each server's tools appear under the prefix `<server>__<tool>` and are
treated like built-ins by the agent.

---

## Project layout

```
main.go                   CLI dispatch, flag parser, system prompt assembly
config/                   provider-agnostic paths (~/.mini)
funcs/                    tools — one file per tool, auto-registered via init()
provider/                 LLM-agnostic interface
provider/{gemini,openai,anthropic}/   backends
mcp/                      MCP client (stdio + http transports)
cli/                      REPL, agent loop, approver, compactor, sessions
```

Adding a new tool: drop a `.go` file in `funcs/` with `func init() { Register(myTool{}) }`.
Adding a new provider: package under `provider/<name>/` with `init()` calling
`provider.Register("<name>", ...)`; blank-import it from `main.go`.

---

## Status

The roadmap up to and including third-party providers + MCP is closed; see
[ROADMAP.md](./ROADMAP.md) for what's deferred (no Windows yet, no SSE
streaming on the HTTP MCP transport).
