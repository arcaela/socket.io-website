# qwen-node

A **browser-free, self-contained Qwen (chat.qwen.ai) guest client** for Node.js with an OpenAI-style API. Runs inside a Docker / Kubernetes pod with only `jsdom` and `undici` as dependencies. No Chromium, no Playwright, no Xvfb.

```js
const qwen = require('./src');

// Simple one-shot
const reply = await qwen([{ role: 'user', content: 'hola' }]);

// Streaming
for await (const ev of qwen.stream('cuenta del 1 al 5')) {
  if (ev.type === 'delta') process.stdout.write(ev.content);
}

// Multi-turn with real server-side memory
const conv = await qwen.conversation({ system: 'Eres conciso.' });
await conv.send('me llamo Ariel');
const { reply: r2 } = await conv.send('¿cómo me llamo?'); // remembers

// Web search, thinking, image gen, artifacts, web_dev
const conv2 = await qwen.conversation({ chatType: 'search' });
const { reply, toolEvents } = await conv2.send('precio bitcoin hoy');
// toolEvents[0].extra.tool_result.docs  ← array of {url, title, snippet, ...}
```

---

## Why this is weird, in one paragraph

Qwen's web frontend protects `/api/v2/chat/completions` with Alibaba's **baxia** anti-bot SDK. Every request must carry three headers — `bx-ua`, `bx-umidtoken`, `bx-v` — that a real browser obtains by running Alibaba's AWSC scripts (`collina.js` + `um.js`). This module boots a `jsdom` window at `require()`-time, evaluates those scripts inside it, and extracts the tokens via the same `window.AWSC.use(...)` API the frontend uses. Tokens are then combined with fresh `acw_tc` cookies from `GET /` and used for pure-Node `fetch()` calls to the chat API.

No private signatures, no custom crypto, no reverse-engineering of baxia's fingerprint algorithm — just "run the real SDK in a fake browser".

---

## Directory layout

```
qwen/
├── README.md              ← you are here
├── package.json           ← main=src/index.js, deps: jsdom + undici
├── server.js              ← standalone HTTP server (Docker entrypoint)
├── build-bundle.js        ← builds dist/ with AWSC scripts embedded inline
└── src/
    ├── index.js           ← public API — this is what you require('./src')
    └── lib/
        ├── constants.js       ← URLs, versions, TTLs, cache paths
        ├── proxy-setup.js     ← wires HTTPS_PROXY into undici (side-effect)
        ├── awsc-scripts.js    ← loads AWSC JS from embed/disk/download
        ├── node-xhr.js        ← XMLHttpRequest → Node fetch polyfill
        ├── jsdom-env.js       ← creates jsdom window + evals AWSC
        ├── tokens.js          ← drives um.init and reads bx-ua/bx-umidtoken
        ├── cookies.js         ← GET / to harvest Cookie header
        ├── session.js         ← cached session lifecycle (25 min TTL)
        ├── sse.js             ← minimal Server-Sent-Events parser
        ├── messages.js        ← OpenAI-style [msgs] → Qwen single prompt
        ├── http.js            ← low-level createChat + streamChatCompletion
        └── conversation.js    ← stateful multi-turn wrapper
```

### What each file does, in one line

| File | Purpose |
|---|---|
| **`src/index.js`** | Public entry point. Wires everything together, runs the require-time jsdom init, exposes `qwen(...)`, `qwen.stream`, `qwen.warmup`, `qwen.conversation`. |
| **`src/lib/constants.js`** | All the hard-coded knobs: `BASE`, `API`, `SPA_VERSION`, `BX_V`, `DEFAULT_MODEL`, `USER_AGENT`, AWSC asset URLs, cache directories, session TTL. No logic, no side effects — safe to require anywhere. |
| **`src/lib/proxy-setup.js`** | Side-effect-only module. On first require, reads `$HTTPS_PROXY` / `$HTTP_PROXY` from the env and installs an `undici.ProxyAgent` as the global dispatcher. Makes Node's `fetch()` honor corporate/sandbox proxies. |
| **`src/lib/awsc-scripts.js`** | Returns the three AWSC script bodies (`awsc.js`, `collina.js`, `um.js`) from three possible sources, in order: (1) an inline `EMBEDDED_AWSC` constant that `build-bundle.js` injects; (2) disk cache at `<CACHE_DIR>/awsc/`; (3) synchronous download via `curl` / `wget` / Node subprocess. |
| **`src/lib/node-xhr.js`** | Factory that returns a drop-in `XMLHttpRequest` polyfill routing requests through Node `fetch()`. Also exposes an `xhrLog` array so other modules can poll for specific responses. |
| **`src/lib/jsdom-env.js`** | `createJsdomEnv(awscScripts)` — builds a `jsdom` window, installs the NodeXHR polyfill, stubs `window.Image`, and evaluates the three AWSC scripts in order. **Must be called synchronously from module-level code.** |
| **`src/lib/tokens.js`** | `acquireTokens(env)` — async. Calls `umMod.init()` which triggers a POST to `https://ynuf.aliapp.org/service/um.json`, polls the xhrLog for the `{tn, id}` response, and reads `uabMod.getUA()` for `bx-ua`. Returns `{ bxUa, umidToken }`. |
| **`src/lib/cookies.js`** | `fetchFreshCookies()` — plain GET to `chat.qwen.ai/` and stitches `Set-Cookie` headers into a single `Cookie` string. Only `acw_tc` and `x-ap` are backend-validated — the rest are just tracker noise we pass through for realism. |
| **`src/lib/session.js`** | Disk-cached session lifecycle. `loadCachedSession()` / `saveSession()` / `makeGetSession({ensureJsdomEnv})` factory returning an async `getSession({forceRefresh})` that coalesces concurrent callers. TTL matches `acw_tc` (25 min). |
| **`src/lib/sse.js`** | `parseSSEBlock(blockText)` — tiny parser for one `data: {json}\n\n` frame. Returns the parsed JSON or `null` on malformed input. |
| **`src/lib/messages.js`** | `flattenMessages([...])` — converts an OpenAI-style messages array (system/user/assistant/...) into a single prompt string that Qwen's one-message-per-turn API can consume. |
| **`src/lib/http.js`** | `baseHeaders(session, extra)`, `createChat(session, opts)`, and `streamChatCompletion(session, chatId, prompt, opts)`. The async iterator yields `created` / `info` / `delta` / `tool` / `finished` events. Throws structured errors for non-SSE responses (RateLimited, Unauthorized, Bad_Request). |
| **`src/lib/conversation.js`** | `makeConversation({getSession})` factory returning `async conversation(opts)` → `{ chatId, turn, history, lastResponseId, send }`. Reuses one `chat_id` across turns and chains `parent_id` for real server-side memory. Returns rich per-turn results including `toolEvents` (citations, image URLs). |
| **`server.js`** | Standalone HTTP server. Calls `qwen.warmup()` on boot, then serves `/healthz`, `/v1/chat/completions`, `/v1/conversations[/:id/messages]`. Auto-rotates session on `RateLimited`. Drop-in Docker entrypoint. |
| **`build-bundle.js`** | Reads AWSC files from the disk cache and emits `dist/src/lib/awsc-scripts.js` with an `EMBEDDED_AWSC = { awsc, collina, um }` literal containing gzipped+base64 versions. The resulting `dist/` can be copied into a container as-is — no network needed at boot for the SDK download. |

---

## The ordering quirk (the subtle thing)

Alibaba's `collina.js` and `um.js` schedule their own `setTimeout` chains at evaluation time. If you `window.eval(...)` them **inside an async function**, those internal timers never fire and subsequent `um.init()` calls silently do nothing — you get no `bx-umidtoken`, no error, just a dead stream.

The fix is simple but load-bearing: **the jsdom setup + AWSC eval must run at the top level of a `require()` call**, not from within a function that has already awaited something. `src/index.js` does this in its `initAtRequireTime()` IIFE, which runs while the CommonJS loader is still executing the module body.

Secondary wrinkle: even with that, the `fetch()` fired from inside our `NodeXHR.prototype.send` fails the first time with "fetch failed" unless we yield the event loop once between require-time and the first `um.init()` call. `tokens.js` does `await new Promise(r => setImmediate(r))` followed by `await new Promise(r => setTimeout(r, 1500))` to handle both the undici dispatcher warm-up and the AWSC internal init delay.

If you see a "umid registration did not return a bx-umidtoken" error, suspect this ordering has been broken by refactoring.

---

## Rate limits and session rotation

Empirically (see the investigation logs outside this directory for raw captures):

- The guest quota is tracked **per `bx-umidtoken`** (device fingerprint), NOT per IP.
- A fresh session lasts somewhere between **2 and ~30 turns** on a shared IP before hitting:
  ```json
  {"code":"RateLimited","details":"You've reached the upper limit for today's usage.","num":6}
  ```
- Calling `qwen.warmup({ forceRefresh: true })` triggers a fresh `um.init` → new `bx-umidtoken` → new device → fresh quota. `qwen()` and `server.js` auto-rotate on `RateLimited` if retry is enabled.
- There's probably also an IP-level cap that eventually kicks in when you rotate too aggressively — we didn't hit it during development, but assume it exists.

---

## What works, what doesn't (empirically verified)

### ✅ Works
- `chat_type: 't2t'` — plain text
- `chat_type: 'search'` — web_search tool auto-invoked, citations in `toolEvents[0].extra.tool_result.docs[]`
- `chat_type: 't2i'` — image generation, result is a signed URL on `cdn.qwenlm.ai`
- `chat_type: 'web_dev'` — HTML/CSS/JS code generation
- `chat_type: 'artifacts'` — structured code artifacts (React components, etc.)
- `feature_config.thinking_enabled: true` — streams a `phase: "think"` before `phase: "answer"`
- `feature_config.thinking_budget: N` — tokens cap for the think phase
- Multi-turn memory via `chat_id` + `parent_id` (`qwen.conversation`)

### ❌ Does NOT work
- `messages[]` of length > 1 → `Bad_Request: Invalid input too many messages.`  (use `qwen.conversation()` or let `flattenMessages` collapse history into one prompt)
- Custom tools at body level (`tools: [...]` OpenAI-style) → silently ignored
- `feature_config.mcp: ['fire-crawl'|'code-interpreter'|...]` → `Internal_Server_Error`  (MCP seems to require an authenticated user, not a guest)
- `temperature`, `max_tokens`, `top_p`, `logprobs`, `stop`, `n` → not used by the frontend and untested; almost certainly ignored server-side

### ⚠️ Untested (other `chat_type` values)
`deep_research`, `deep_research_webdev`, `learn`, `travel`, `slides`, `aipodcast`, `translate`, `image_edit`, `t2v`, `i2v` — they exist in the frontend enum and `GET /api/v2/configs/` advertises small guest quotas for most of them, but we did not exercise them end-to-end.

---

## Request / response shapes

See the "Especificación completa del API" section in the project notes — it's the exhaustive reference with every field, phase, and enum value decoded. Short version:

```jsonc
// POST /api/v2/chat/completions?chat_id=<uuid>
{
  "stream": true,
  "version": "2.1",
  "incremental_output": true,
  "chat_id": "<uuid>",
  "chat_mode": "normal",            // "normal" | "guest" | "community" | "local"
  "model": "qwen3.6-plus",
  "parent_id": null,                 // chain to previous turn's response_id
  "timestamp": 1775934519,            // ⚠ UNIX seconds (chats/new uses ms)
  "messages": [
    // EXACTLY ONE element — Qwen rejects multi-message arrays
    {
      "role": "user",
      "content": "...",
      "chat_type": "t2t",            // t2t|search|thinking|t2i|web_dev|artifacts|...
      "sub_chat_type": "t2t",
      "feature_config": {
        "thinking_enabled": false,
        "output_schema": "phase",
        "thinking_budget": 38912     // optional
      },
      "extra": {}
    }
  ]
}
```

Response stream events (all `data: {json}\n\n`):

```jsonc
// Opening
{"response.created": {"chat_id": "...", "parent_id": "...", "response_id": "..."}}

// Keep-alive (image gen, long think)
{"response.info": {"action": "keep_alive", "chat_id": "...", "response_id": "...", "timestamp": 1775934509}}

// Text delta (phase = "answer" | "think" | ...)
{"choices": [{"delta": {"role": "assistant", "content": "hola", "phase": "answer", "status": "typing"}}], "response_id": "...", "usage": {"input_tokens": 554, "output_tokens": 5, "total_tokens": 559, ...}, "timestamp": 1775934589}

// Tool CALL (model asks the backend to run a native tool)
{"choices": [{"delta": {"role": "assistant", "content": "", "phase": "web_search", "status": "typing", "function_call": {"name": "web_search", "arguments": "{\"queries\": [\"...\"]}"}, "function_id": "round_0_1", "extra": {"display_position": "answer"}}}], ...}

// Tool RESULT (backend returns what it found)
{"choices": [{"delta": {"role": "function", "content": "", "phase": "web_search", "status": "finished", "name": "web_search", "extra": {"function_id": "1", "tool_result": {"docs": [{"url": "...", "title": "...", "snippet": "...", "date": ""}, ...]}}}}]}

// Finish
{"choices": [{"delta": {"content": "", "role": "assistant", "status": "finished", "phase": "answer"}}], "response_id": "..."}

// Error envelope (200 OK, application/json, NOT SSE)
{"success": false, "request_id": "...", "data": {"code": "RateLimited", "details": "You've reached the upper limit for today's usage.", "template": "...{{num}} hours...", "num": 6}}
```

---

## Usage from the HTTP server

```bash
# Boot (warmup is automatic)
node server.js                                      # listens on PORT=8787

# Stateless single-turn
curl -X POST http://localhost:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","content":"hola"}]}'

# Stateful multi-turn
CID=$(curl -s -X POST http://localhost:8787/v1/conversations \
  -H 'Content-Type: application/json' \
  -d '{"system":"Eres conciso."}' | jq -r .id)
curl -X POST http://localhost:8787/v1/conversations/$CID/messages \
  -H 'Content-Type: application/json' \
  -d '{"content":"me llamo Ariel"}'
curl -X POST http://localhost:8787/v1/conversations/$CID/messages \
  -H 'Content-Type: application/json' \
  -d '{"content":"¿cómo me llamo?"}'

# Health check
curl http://localhost:8787/healthz
```

---

## Production checklist

- [ ] **Run `node build-bundle.js`** once so the AWSC scripts are inlined — makes your Docker image self-contained.
- [ ] **Put this server behind a gateway with auth.** `server.js` has no authentication; anyone with network access can burn your guest quota.
- [ ] **Mount `~/.cache/qwen-node` as a writable volume** (or set `QWEN_CACHE_DIR` somewhere writable). Saves ~10 s of re-init on every restart.
- [ ] **Set `HTTPS_PROXY`** in the pod environment if the cluster has an egress proxy. `src/lib/proxy-setup.js` will pick it up automatically.
- [ ] **Monitor for `RateLimited` responses.** If you see `retryAfterHours > 0` consistently, rotation isn't helping and you need a different egress IP.
- [ ] **Be aware this may violate Qwen's ToS.** Guest endpoints are not meant for programmatic access. Use for research/PoC only, not production-facing products. A real API key (Qwen API, OpenRouter, DeepSeek, etc.) is cheap and eliminates this entire class of risk.
- [ ] **Pin the AWSC script versions** (see `AWSC_URLS` in `src/lib/constants.js`). If Alibaba upgrades `collina` or `um` and the new format is rejected, regenerate the bundle and re-pin.

---

## Requirements

- Node.js **22+** (needs built-in `fetch` and `Headers.getSetCookie()`)
- `jsdom` ^22
- `undici` ^6
- One of: `curl`, `wget`, or nothing (fallback uses a spawned Node subprocess) — only needed on the first run when `EMBEDDED_AWSC` is null.

No Chromium. No Playwright. No browser. No headless anything.
