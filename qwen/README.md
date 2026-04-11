# qwen-node

A **browser-free, self-contained Qwen (chat.qwen.ai) guest client** for Node.js with an OpenAI-style API. Runs inside a Docker / Kubernetes pod with only `jsdom` and `undici` as dependencies. No Chromium, no Playwright, no Xvfb.

```js
const qwen = require('./src');

// Every method has TWO forms: Promise (collected) and Stream (async iterator).
// Both throw the SAME typed exceptions on failure — nothing goes through
// stream chunks as an "error event".

// ── Promise form ──
const reply                  = await qwen.ask('¿Capital de Francia?');         // → string
const { reply, sources }     = await qwen.search('precio bitcoin hoy');         // → { reply, sources, usage }
const { url, width, height } = await qwen.image('un gato naranja');             // → { url, width, height, model }
const { reply, thinking }    = await qwen.think('explica la relatividad');      // → { reply, thinking, usage }

// ── Stream form ──
for await (const ev of qwen.ask.stream('contá del 1 al 5')) {
  if (ev.type === 'text') process.stdout.write(ev.content);
  if (ev.type === 'done') console.log('\nusage:', ev.usage);
}

// ── Stateful chat: all four methods under one chat_id with shared memory ──
const chat = await qwen.chat({ model: 'qwen3.6-plus', system: 'Sé conciso.' });

await chat.ask('me llamo Ariel');
const { reply }              = await chat.ask('¿cómo me llamo?');               // remembers
const { reply, sources }     = await chat.search('clima en Madrid');             // this turn → web search
const { url, width, height } = await chat.image('un paisaje futurista');         // this turn → image gen
const { reply, thinking }    = await chat.think('diseñá una SPA paso a paso');   // this turn → thinking

// Every chat method also has .stream
for await (const ev of chat.ask.stream('resumí todo')) { /* ... */ }
```

Full runnable examples live under [`examples/`](./examples/).

---

## Why this is weird, in one paragraph

Qwen's web frontend protects `/api/v2/chat/completions` with Alibaba's **baxia** anti-bot SDK. Every request must carry three headers — `bx-ua`, `bx-umidtoken`, `bx-v` — that a real browser obtains by running Alibaba's AWSC scripts (`collina.js` + `um.js`). This module boots a `jsdom` window at `require()`-time, evaluates those scripts inside it, and extracts the tokens via the same `window.AWSC.use(...)` API the frontend uses. Tokens are then combined with fresh `acw_tc` cookies from `GET /` and used for pure-Node `fetch()` calls to the chat API.

No private signatures, no custom crypto, no reverse-engineering of baxia's fingerprint algorithm — just "run the real SDK in a fake browser".

---

## Practical API reference

### Design principles

1. **`qwen.chat()` is the primitive.** Every top-level helper (`qwen.ask`, `qwen.search`, `qwen.image`, `qwen.think`) is a one-line wrapper that creates a throwaway `chat()` and invokes the matching method. The session / credentials / HTTP code lives in exactly one place.

2. **Every method has two forms**: a **Promise form** (the default — you `await` it and get the final result) and a **Stream form** (accessed via the `.stream` property — you `for await` it and receive typed events). Both forms go through the same internal SSE parser, so they share identical behavior.

3. **Errors are always thrown exceptions, never stream chunks.** Both the Promise form and the Stream form throw the same typed exception classes (`QwenRateLimitedError`, `QwenUnauthorizedError`, etc.) on failure. Stream consumers wrap `for await` in `try/catch` exactly like Promise consumers wrap `await`. If the iterator emitted a `done` event, the request succeeded. If it didn't, the `for await` threw.

```js
// These two snippets are literally equivalent:
const reply = await qwen.ask('hola');
// ==
const reply = (await (await qwen.chat()).ask('hola')).reply;
```

---

### Method contract

| Method | Input | Promise form returns | Stream form events |
|---|---|---|---|
| `qwen.ask` / `chat.ask` | `(prompt, opts?)` | `string` / `{reply, turn, usage, thinking}` | `start → text → done` |
| `qwen.search` / `chat.search` | `(query, opts?)` | `{reply, sources, usage}` / `{reply, sources, turn, usage}` | `start → tool_call → sources → text → done` |
| `qwen.image` / `chat.image` | `(prompt, opts?)` | `{url, width, height, model}` | `start → info → image → done` |
| `qwen.think` / `chat.think` | `(prompt, opts?)` | `{reply, thinking, usage}` / `{reply, thinking, turn, usage}` | `start → thinking → text → done` |

**All four methods**:
- Accept a plain string as the first argument.
- Accept an `opts` object with at minimum `{ model?, system?, onDelta? }` for the top-level forms and `{ chatType?, chatMode?, thinking? }` for chat methods.
- Have a `.stream` property that returns an async iterator over typed events.
- Throw a `QwenError` subclass on failure (see Error handling below).

---

### Stream event types

Every event has a `type` field. Consumers switch on it. The stream always terminates with a single `done` event carrying the aggregated result — if the stream ends without `done`, something threw.

```js
type QwenEvent =
  | { type: 'start',      chatId, responseId, parentId }
  | { type: 'thinking',   content, fullThinking }                        // phase === 'think'
  | { type: 'text',       content, fullContent, phase }                  // phase === 'answer' / other text
  | { type: 'tool_call',  name, arguments, phase, functionId }           // web_search function streaming
  | { type: 'sources',    sources: [{url, title, snippet, date, hostname}] }
  | { type: 'image',      url, width, height, extra }                    // phase === 'image_gen'
  | { type: 'info',       info: { action: 'keep_alive', ... } }          // server keep-alive
  | { type: 'done',       reply, thinking, sources, image, usage,
                          responseId, parentId, turn, toolEvents }
```

A tighter, method-specific view of what you'll actually see:

```
qwen.ask.stream(prompt)     → start → text × N → done
qwen.think.stream(prompt)   → start → thinking × N → text × N → done
qwen.search.stream(query)   → start → tool_call × N → sources → text × N → done
qwen.image.stream(prompt)   → start → info × N → image → done
```

---

### Error handling

All errors thrown by the library inherit from `QwenError`. Five subclasses cover every observed backend failure mode:

| Class | `.code` | When it happens |
|---|---|---|
| `QwenRateLimitedError` | `RateLimited` | Daily guest quota hit on the current `bx-umidtoken`. `.retryAfterHours` hints when to retry (usually 6). Helpers auto-rotate once and retry transparently. |
| `QwenUnauthorizedError` | `Unauthorized` | Tokens expired or invalid. Helpers auto-rotate once. |
| `QwenBadRequestError` | `Bad_Request` | Client-side bug — malformed body, unsupported field combination, invalid chat_id. Not retried. |
| `QwenServerError` | `Internal_Server_Error` | Backend error. Usually means you tried an unsupported guest feature (e.g., MCP tools). Not retried. |
| `QwenNetworkError` | `NetworkError` | `fetch` failed at the transport layer (DNS, TLS, proxy). Wraps the original error on `.cause`. |

**Use `instanceof` or `.code` — whichever is more ergonomic:**

```js
try {
  const reply = await qwen.ask('hola');
} catch (e) {
  if (e instanceof qwen.QwenRateLimitedError) {
    console.log(`retry in ${e.retryAfterHours}h`);
  } else if (e.code === 'BadRequest') {
    console.log('client bug:', e.message);
  }
}

// Identical for streams — the iterator throws during for-await:
try {
  for await (const ev of qwen.ask.stream('hola')) {
    if (ev.type === 'text') process.stdout.write(ev.content);
  }
} catch (e) {
  if (e instanceof qwen.QwenRateLimitedError) { /* ... */ }
}
```

All typed errors expose the same fields:
```js
e.code              // 'RateLimited' | 'Unauthorized' | 'Bad_Request' | ...
e.status            // HTTP status if applicable
e.retryAfterHours   // server hint for RateLimited (usually 6)
e.response          // parsed envelope from the server
e.cause             // original native error (QwenNetworkError only)
```

**Auto-retry**: the top-level helpers (`qwen.ask`, `qwen.search`, `qwen.image`, `qwen.think`) automatically rotate the session on `RateLimited` / `Unauthorized` and retry the request once. The chat methods do NOT auto-retry (rotating would create a different `chat_id` and lose memory). If you want retry with a chat, catch the error yourself and rebuild the chat:

```js
try {
  await chat.ask('...');
} catch (e) {
  if (e instanceof qwen.QwenRateLimitedError) {
    await qwen.warmup({ forceRefresh: true });
    chat = await qwen.chat({ ...originalOpts });  // new chat_id, fresh quota
    await chat.ask('...');
  }
}
```

---

### `qwen.chat(opts?)` → stateful handle

The core primitive. Creates a server-side conversation and returns a handle with four turn methods.

```js
const chat = await qwen.chat({
  model:    'qwen3.6-plus',   // optional — see GET /api/models for full list
  system:   'Sos un asistente muy breve.',
  chatType: 't2t',             // default mode for .ask() turns
  thinking: false,              // default for .ask() turns
});
```

**Turn methods** — all four accept the same kind of message and return richer structured data:

| Method | Chat type used | Returns |
|---|---|---|
| `chat.ask(msg)` | the chat's default (`t2t` usually) | `{ reply, turn, usage, thinking? }` |
| `chat.search(query)` | `search` (forces web_search tool) | `{ reply, sources, turn, usage }` |
| `chat.image(prompt)` | `t2i` (forces image_gen tool) | `{ url, width, height, model }` |
| `chat.think(msg)` | whatever the chat uses, but `thinking_enabled=true` | `{ reply, thinking, turn, usage }` |

All four can be **freely mixed inside the same chat**. The backend's memory is phase-agnostic — a `chat.think()` turn and a later `chat.search()` turn both see the full history of every prior turn regardless of type.

```js
const chat = await qwen.chat({ system: 'Sé útil.' });

// 1) Think + plan
await chat.think('Diseñá una web SPA para administrar tareas.');

// 2) Search for references with full context of turn 1
await chat.search('qué framework de SPA es más popular hoy');

// 3) Ask for implementation, still seeing turns 1 and 2
const { reply } = await chat.ask('Implementá el esqueleto en HTML + JS puro.');

// 4) Generate an icon for the app
const { url } = await chat.image('un ícono minimalista para una app de tareas, azul');

// Inspect state
chat.turn            // 4
chat.history         // [{role:'user',...}, {role:'assistant',...}, ...]
chat.chatId          // server-side conversation id (reusable across process restarts via parentId)
chat.lastResponseId  // latest server-generated response id
```

### Controlling the model and defaults

The chat options set the **defaults** for every subsequent turn. You can still override on a per-turn basis:

```js
const chat = await qwen.chat({ model: 'qwen3.5-plus', chatType: 'artifacts' });

await chat.ask('genera un componente React');  // uses chatType: 'artifacts'
await chat.ask('ahora en Vue', { chatType: 'web_dev' }); // per-turn override
```

**Available models** (from `GET /api/models`):
- `qwen3.6-plus` — latest, supports all modes including thinking, search, t2i, t2v, deep_research
- `qwen3.5-plus` — previous generation, same feature surface
- `qwen3.5-omni-plus` — multimodal (audio/video understanding) but no thinking

**Enabling tools per chat:** Qwen's "tools" are selected per turn via `chatType`, not pre-enabled at the chat level. If you want a chat where every `.ask()` turn auto-triggers web search, set `chatType: 'search'` when creating the chat and `ask()` will behave like `search()`. See the limitations section below for MCP tools (fire-crawl, code-interpreter) — those are NOT available to guest sessions.

---

### `qwen.warmup({ forceRefresh?: boolean }?)` → `{ ok, createdAt, expiresIn }`

Pre-warms the session so the first user request doesn't pay the jsdom + umid-server boot cost (~15 s on cold start). Call this once during container startup.

```js
await qwen.warmup();                        // use cache if valid
await qwen.warmup({ forceRefresh: true });  // force a new bx-umidtoken (new quota!)
```

This is also your escape hatch for `RateLimited` errors — `forceRefresh: true` mints a new device token and resets the per-device guest quota.

---

## Lower-level API (still exposed)

For advanced use cases — building your own server, integrating with OpenAI SDK clients, streaming at the event level, etc.:

```js
// OpenAI-style messages array → single Qwen turn (history is flattened to a transcript)
await qwen(messages, { model?, system?, chatType?, onDelta? });

// Same but returns an async iterator over raw SSE events (type: created|info|delta|tool|finished)
for await (const ev of qwen.stream(messages, opts)) {
  if (ev.type === 'delta')  { /* text token */ }
  if (ev.type === 'tool')   { /* ev.extra.tool_result.docs / functionCall */ }
  if (ev.type === 'finished') { break; }
}

// Raw conversation handle (the primitive that .chat() wraps)
const conv = await qwen.conversation({ chatType: 'search' });
const r = await conv.send('...');
// r.toolEvents[] contains the full raw tool-call / tool-result records

// Direct session access
const session = await qwen.getSession();
// → { createdAt, userAgent, cookieHeader, bxUa, bxUmidtoken, bxV }
```

---

## Limitations (verified empirically)

| Limitation | Effect | Workaround |
|---|---|---|
| `messages[]` must have exactly 1 item | Multi-message arrays return `Bad_Request` | Use `qwen.chat()` for real memory, or let `qwen(messages, ...)` flatten history into a single prompt |
| Custom OpenAI-style `tools: [...]` at body level | Silently ignored, no tool calls happen | Use the built-in tools via `chatType`: `'search'`, `'t2i'`, `'web_dev'`, `'artifacts'` |
| `feature_config.mcp: [...]` (fire-crawl, code-interpreter, amap) | Returns `Internal_Server_Error` for guest sessions | Not supported for guests. Needs authenticated user. |
| `temperature`, `max_tokens`, `top_p`, `n`, `stop`, `logprobs` | Not sent by the frontend — probably ignored by the backend | Untested. Don't rely on them. |
| Daily rate limit per `bx-umidtoken` | Returns `RateLimited` with `retryAfterHours: 6` after a few turns | Call `qwen.warmup({ forceRefresh: true })` to rotate the device token (auto on `RateLimited` for the helpers) |
| Probable per-IP ceiling (not hit during development) | Even rotation stops working from one IP | Distribute egress across multiple IPs |
| Signed image URLs expire | `cdn.qwenlm.ai` PNG links become invalid after a few hours | Download the bytes immediately in your code |

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
        ├── errors.js          ← QwenError hierarchy (typed exceptions)
        ├── http.js            ← low-level createChat + streamChatCompletion
        ├── conversation.js    ← stateful multi-turn (send + stream)
        └── helpers.js         ← chat/ask/search/image/think + .stream
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
| **`src/lib/errors.js`** | Typed `QwenError` hierarchy (`QwenRateLimitedError`, `QwenUnauthorizedError`, `QwenBadRequestError`, `QwenServerError`, `QwenNetworkError`) + `errorFromEnvelope()` / `wrapNetworkError()` factories. Every helper and stream throws one of these on failure — never a plain `Error`. |
| **`src/lib/http.js`** | `baseHeaders(session, extra)`, `createChat(session, opts)`, and `streamChatCompletion(session, chatId, prompt, opts)`. The async iterator yields `created` / `info` / `delta` / `tool` / `finished` events. Throws typed `QwenError` subclasses for non-SSE responses (RateLimited, Unauthorized, Bad_Request, Internal_Server_Error, NetworkError). |
| **`src/lib/conversation.js`** | `makeConversation({getSession})` factory returning `async conversation(opts)` → `{ chatId, turn, history, lastResponseId, send, stream }`. Both forms go through the same internal `_sendStream` generator that emits typed events (`start`, `text`, `thinking`, `tool_call`, `sources`, `image`, `info`, `done`). `.send()` collects the stream to a done payload; `.stream()` exposes the iterator. |
| **`src/lib/helpers.js`** | `makeHelpers({ conversation })` — builds the practical API surface. `chat()` is the primitive, returning a handle with `ask/search/image/think` methods each exposing a `.stream` property for the iterator form. The top-level `ask/search/image/think` are 1-line wrappers that create a throwaway chat. |
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
