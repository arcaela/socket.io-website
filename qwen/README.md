# qwen-node

A **browser-free, self-contained Qwen (chat.qwen.ai) guest client** for Node.js with a single-class TypeScript API. Runs inside a Docker / Kubernetes pod with only `jsdom` and `undici` as runtime dependencies. No Chromium, no Playwright, no Xvfb.

```ts
import Qwen from 'qwen-node';

// ── Static shortcuts (throwaway chat under the hood) ──
const reply                  = await Qwen.ask('¿Capital de Francia?');         // → string
const { reply, sources }     = await Qwen.search('precio bitcoin hoy');         // → { reply, sources, usage }
const { url, width, height } = await Qwen.image('gato naranja');                // → { url, width, height, model }
const { reply, thinking }    = await Qwen.think('explica la relatividad');      // → { reply, thinking, usage }

// ── Instance: a real stateful chat ──
const chat = new Qwen({
  model:    'qwen3.6-plus',
  system:   'Sé conciso.',
  stream:   false,                 // default: promise form
});

await chat.ask('me llamo Ariel');
const { reply }              = await chat.ask('¿cómo me llamo?');               // remembers
const { reply, sources }     = await chat.search('clima en Madrid');            // this turn → web search
const { url, width, height } = await chat.image('un paisaje futurista');        // this turn → image gen
const { reply, thinking }    = await chat.think('diseñá una SPA paso a paso');  // this turn → thinking

chat.usage      // { input, output, tokens, requests }  — accumulated across turns
chat.chatId     // server-side chat identifier
chat.history    // local transcript

// ── Stream mode: construct once, every method becomes an async iterator ──
const streamChat = new Qwen({ stream: true, system: 'Brief.' });
for await (const ev of streamChat.ask('count to 5')) {
  if (ev.type === 'text') process.stdout.write(ev.content);
  if (ev.type === 'done') console.log('\nusage:', streamChat.usage);
}

// ── Session recovery: export state, resume later ──
const snapshot = await chat.exportSession();
// ... persist snapshot somewhere (Redis, DB, file, ...) ...
const resumed = new Qwen({
  ...snapshot.options,
  chatId:         snapshot.chatId,
  lastResponseId: snapshot.lastResponseId,
  history:        snapshot.history,
  usage:          snapshot.usage,
  session:        snapshot.session,
});
await resumed.ask('continuación'); // remembers everything
```

Full runnable examples live under [`examples/`](./examples/).

---

## Installation

```bash
npm install
npm run build       # compiles src/**/*.ts → dist/
```

Requires Node.js 22+.

---

## Why this exists, in one paragraph

Qwen's web frontend protects `/api/v2/chat/completions` with Alibaba's **baxia** anti-bot SDK. Every request must carry three headers — `bx-ua`, `bx-umidtoken`, `bx-v` — that a real browser obtains by running Alibaba's AWSC scripts (`collina.js` + `um.js`). This module boots a `jsdom` window at `require()`-time, evaluates those scripts inside it, and extracts the tokens via the same `window.AWSC.use(...)` API the frontend uses. Tokens + fresh `acw_tc` cookies are then combined for pure Node `fetch()` calls to the chat API.

No private signatures, no custom crypto, no reverse-engineering of baxia's fingerprint algorithm — just "run the real SDK in a fake browser".

---

## The `Qwen` class

The whole library is a single default export:

```ts
import Qwen from 'qwen-node';
```

### Constructor

```ts
new Qwen(options?: QwenOptions)
```

Options control both the **chat behavior** and **session recovery**:

```ts
interface QwenOptions {
  // ── behavior ──
  stream?:  boolean;                   // default: false
  model?:   QwenModel;                  // default: 'qwen3.6-plus'
  system?:  string;                     // injected on first turn only
  chatMode?: 'normal' | 'guest' | 'community' | 'local';
  chatType?: ChatType;                  // default mode for .ask() turns
  thinking?: boolean;                   // enable thinking on .ask() turns

  // ── session recovery ──
  chatId?:         string;              // existing server-side chat id
  lastResponseId?: string;              // last parent_id for chaining
  session?:        SavedSession;        // credentials from chat.exportSession()
  history?:        Message[];           // local transcript to re-hydrate
  usage?:          QwenUsage;           // prior usage counters
}
```

### Instance methods

Every method returns either a `Promise<Result>` (default) or an `AsyncGenerator<StreamEvent>` (when `stream: true`). Both forms throw the SAME typed exceptions on failure — errors are never delivered as stream chunks.

| Method | Chat type used | Promise result | Stream events |
|---|---|---|---|
| `chat.ask(msg)` | default (`t2t`) | `{ reply, turn, usage, thinking? }` | `start → text → done` |
| `chat.search(q)` | `search` | `{ reply, sources, turn, usage }` | `start → tool_call → sources → text → done` |
| `chat.image(p)` | `t2i` | `{ url, width, height, model, turn }` | `start → info → image → done` |
| `chat.think(msg)` | current + thinking | `{ reply, thinking, turn, usage }` | `start → thinking → text → done` |

### Instance state

```ts
chat.chatId           // string — server-side conversation id
chat.lastResponseId    // string — last turn's response_id (parent_id chain)
chat.usage             // { input, output, tokens, requests }
chat.history           // [{role, content, thinking?}, ...]
chat.options           // (frozen) QwenOptions passed to the constructor
chat.stream            // boolean — whichever `stream` was set
```

### Persistence

```ts
chat.toJSON()                  // synchronous snapshot (no session credentials)
await chat.exportSession()     // full snapshot WITH session credentials
```

Pass a snapshot back to `new Qwen({ ...snapshot })` to resume.

### Static methods

```ts
// One-shot shortcuts (same signatures as instance methods)
await Qwen.ask(prompt, opts?)           // → string
await Qwen.search(query, opts?)         // → SearchResult
await Qwen.image(prompt, opts?)         // → ImageResult
await Qwen.think(prompt, opts?)         // → ThinkResult

// Session management
await Qwen.warmup({ forceRefresh? })    // → { ok, createdAt, expiresIn }

// Model discovery
Qwen.models                             // → ['qwen3.6-plus', 'qwen3.5-plus', 'qwen3.5-omni-plus']
await Qwen.fetchModels()                // → live list from /api/models with capabilities

// Error classes (all extend QwenError)
Qwen.QwenError
Qwen.QwenRateLimitedError
Qwen.QwenUnauthorizedError
Qwen.QwenBadRequestError
Qwen.QwenServerError
Qwen.QwenNetworkError
```

The second argument to a static method is a full `QwenOptions` — it's used to construct a throwaway instance internally.

---

## Stream event types

When `stream: true`, every method returns an async iterator that yields discriminated-union events. The iterator always terminates with a single `done` event carrying the aggregated result; if it ended without `done`, the `for await` threw.

```ts
type StreamEvent =
  | { type: 'start';     chatId: string; responseId: string | null; parentId: string | null }
  | { type: 'thinking';  content: string; fullThinking: string }                        // phase=think
  | { type: 'text';      content: string; fullContent: string; phase?: string }         // phase=answer
  | { type: 'tool_call'; name: string; arguments: string; phase: string; functionId: string }
  | { type: 'sources';   sources: Source[] }                                            // web_search docs
  | { type: 'image';     url: string; width: number | null; height: number | null }
  | { type: 'info';      info: { action: 'keep_alive', ... } }                          // during image gen
  | { type: 'done';      reply, thinking, sources, image, usage, turn, toolEvents, ... };
```

### What you'll see per method

```
ask.stream    → start → text × N → done
think.stream  → start → thinking × N → text × N → done
search.stream → start → tool_call × N → sources → text × N → done
image.stream  → start → info × N → image → done
```

---

## Error handling

All failures throw typed exceptions that inherit from `Qwen.QwenError`. Use `instanceof` or `.code` — both work.

| Class | `.code` | When |
|---|---|---|
| `QwenRateLimitedError` | `RateLimited` | Daily guest quota hit on the current `bx-umidtoken`. Has `.retryAfterHours` (usually 6). |
| `QwenUnauthorizedError` | `Unauthorized` | Session tokens invalid / expired. |
| `QwenBadRequestError` | `Bad_Request` | Client bug — malformed body, invalid chat_id, unsupported combination. |
| `QwenServerError` | `Internal_Server_Error` | Backend error, usually an unsupported guest feature (e.g. MCP). |
| `QwenNetworkError` | `NetworkError` | `fetch` failed at the transport layer. Original error on `.cause`. |

```ts
try {
  const reply = await Qwen.ask('hola');
} catch (e) {
  if (e instanceof Qwen.QwenRateLimitedError) {
    await Qwen.warmup({ forceRefresh: true });    // new device → new quota
    // ... retry ...
  } else if (e instanceof Qwen.QwenBadRequestError) {
    console.error('client bug:', e.message);
  } else {
    throw e;
  }
}
```

The same pattern works for streams — `for await` throws during iteration:

```ts
try {
  for await (const ev of streamChat.ask('hola')) {
    if (ev.type === 'text') process.stdout.write(ev.content);
  }
} catch (e) {
  if (e instanceof Qwen.QwenRateLimitedError) { /* ... */ }
}
```

---

## Session recovery in detail

The most practical use case: a web service receives a user message, needs to continue an existing conversation across process restarts or load-balanced workers.

```ts
// During the first request, the service creates a chat and stores a snapshot
const chat = new Qwen({ system: 'You are helpful.' });
await chat.ask('My favorite color is blue');
const snapshot = await chat.exportSession();
await redis.set(`qwen:${userId}`, JSON.stringify(snapshot), 'EX', 1500); // 25 min

// During a follow-up request (possibly from a different worker)
const saved = JSON.parse(await redis.get(`qwen:${userId}`));
const resumed = new Qwen({
  ...saved.options,
  chatId:         saved.chatId,
  lastResponseId: saved.lastResponseId,
  history:        saved.history,
  usage:          saved.usage,
  session:        saved.session,
});
const { reply } = await resumed.ask('What was my favorite color?');  // → "Blue"
```

**Caveats:**
- The `session` credentials expire after 25 minutes. Older snapshots will fail with `QwenUnauthorizedError` or `QwenBadRequestError` — catch and create a fresh chat as a fallback.
- Qwen tracks conversation history on the backend by `chat_id`, so `resumed.ask()` sees all prior turns even though the local `history` is never sent on the wire.
- If you don't pass `session`, the library will mint a fresh one from its own cache. Qwen's backend MAY accept the existing `chat_id` under a different session — empirically this works inside the TTL window — but don't rely on it.

---

## Directory layout

```
qwen/
├── README.md                ← you are here
├── package.json             ← main=dist/index.js, types=dist/index.d.ts
├── tsconfig.json            ← TypeScript compiler config
├── server.js                ← standalone HTTP server (Docker entrypoint)
├── build-bundle.js          ← builds dist/ with AWSC scripts embedded inline
├── examples/                ← runnable .js examples (use the compiled dist/)
│   ├── 01-ask.js
│   ├── 02-search.js
│   ├── 03-image.js
│   ├── 04-think.js
│   ├── 05-chat.js
│   ├── 06-errors.js
│   └── 07-session-recovery.js
├── src/                     ← TypeScript source (committed)
│   ├── index.ts             ← the Qwen class, public API
│   └── lib/
│       ├── types.ts             ← shared interfaces & types
│       ├── constants.ts         ← URLs, versions, TTLs, cache paths
│       ├── proxy-setup.ts       ← wires HTTPS_PROXY into undici (side-effect)
│       ├── awsc-scripts.ts      ← load AWSC JS from embed/disk/sync-download
│       ├── node-xhr.ts          ← XMLHttpRequest → Node fetch polyfill
│       ├── jsdom-env.ts         ← creates jsdom window + evals AWSC
│       ├── tokens.ts            ← drives um.init and reads bx-ua/bx-umidtoken
│       ├── cookies.ts           ← GET / to harvest Cookie header
│       ├── session.ts           ← session cache lifecycle (25 min TTL)
│       ├── sse.ts               ← minimal Server-Sent-Events parser
│       ├── errors.ts            ← QwenError hierarchy (typed exceptions)
│       ├── http.ts              ← createChat + streamChatCompletion (throws typed)
│       └── conversation.ts      ← stateful send() + stream() dual mode
└── dist/                    ← compiled JavaScript + .d.ts (gitignored)
```

### What each file does, in one line

| File | Purpose |
|---|---|
| **`src/index.ts`** | Public entry point. Defines `export default class Qwen` with static methods (`ask`, `search`, `image`, `think`, `warmup`, `fetchModels`), instance methods, `models` static property, usage tracking, session recovery. Module-level side effect: runs require-time jsdom setup. |
| **`src/lib/types.ts`** | All shared TypeScript types and interfaces: `QwenOptions`, `QwenState`, `QwenUsage`, `Source`, `StreamEvent` (discriminated union), result shapes, enums. |
| **`src/lib/constants.ts`** | `BASE`, `API`, `SPA_VERSION`, `BX_V`, `DEFAULT_MODEL`, `USER_AGENT`, `KNOWN_MODELS`, `AWSC_URLS`, cache paths, `SESSION_TTL_MS`. |
| **`src/lib/proxy-setup.ts`** | Side-effect-only module. On first import, reads `$HTTPS_PROXY`/`$HTTP_PROXY` and installs an `undici.ProxyAgent` as the global dispatcher. |
| **`src/lib/awsc-scripts.ts`** | Resolves the AWSC bundle bodies from three sources in order: inline embedded constant (bundle), disk cache, synchronous download (curl/wget/Node subprocess). |
| **`src/lib/node-xhr.ts`** | `XMLHttpRequest` polyfill that routes through Node `fetch()`. Used inside jsdom so `um.js` can POST to `ynuf.aliapp.org`. |
| **`src/lib/jsdom-env.ts`** | `createJsdomEnv(scripts)` — builds a jsdom window, installs NodeXHR, stubs `window.Image`, evaluates the three AWSC scripts. **Must be called from module-top synchronous code** (see ordering quirk). |
| **`src/lib/tokens.ts`** | `acquireTokens(env)` — drives `um.init()` which POSTs to `ynuf.aliapp.org/service/um.json`, polls xhrLog for `{tn}`, reads `uab.getUA()` for `bx-ua`. Returns `{bxUa, umidToken}`. |
| **`src/lib/cookies.ts`** | `fetchFreshCookies()` — GETs `chat.qwen.ai/` and builds a `Cookie` header from the `Set-Cookie` response. |
| **`src/lib/session.ts`** | Disk-cached session lifecycle. `loadCachedSession()`, `saveSession()`, `injectSession()` (for recovery), `makeGetSession({ensureJsdomEnv})` factory returning an async `getSession({forceRefresh})` that coalesces concurrent callers. |
| **`src/lib/sse.ts`** | `parseSSEBlock(block)` — parses one `data: {json}\n\n` SSE frame. |
| **`src/lib/errors.ts`** | `QwenError` + five subclasses, `errorFromEnvelope(parsed, text)` dispatcher, `wrapNetworkError(err, ctx)` helper. |
| **`src/lib/http.ts`** | `baseHeaders(session, extra)`, `createChat(session, opts)`, `streamChatCompletion(session, chatId, prompt, opts)` async generator. Throws typed `QwenError` subclasses. |
| **`src/lib/conversation.ts`** | `makeConversation({getSession})` factory. The returned conversation exposes both `send()` (Promise form) and `stream()` (async iterator form), sharing one internal `_sendStream` generator that emits typed `StreamEvent`s. State mutation (turn++, history push, lastResponseId) happens right before the `done` event is yielded. |

---

## Limitations (verified empirically)

| Limitation | Effect | Workaround |
|---|---|---|
| `messages[]` must have exactly 1 item | `Bad_Request: Invalid input too many messages.` | Use `qwen.chat()` / `new Qwen(...)` for real memory, or let the server flatten history into a single prompt. |
| Custom `tools: [...]` (OpenAI-style) | Silently ignored | Use the built-in tools via `chatType`: `search`, `t2i`, `web_dev`, `artifacts`, etc. |
| `feature_config.mcp: [...]` | `Internal_Server_Error` for guests | Not supported for guests — needs authenticated user. |
| Daily rate limit per `bx-umidtoken` | `RateLimited` with `retryAfterHours: 6` after a few turns | `Qwen.warmup({ forceRefresh: true })` rotates the device token. |
| Signed image URLs expire | `cdn.qwenlm.ai` PNG links die after some hours | Download the bytes immediately. |
| Session TTL ~25 minutes | `acw_tc` cookie expires | Cache layer refreshes automatically; saved sessions older than TTL fail with `Unauthorized`. |
| Probable per-IP ceiling (not hit in dev) | Rotation eventually stops working from one IP | Distribute egress across multiple IPs. |

---

## The ordering quirk

Alibaba's `collina.js` and `um.js` schedule their own `setTimeout` chains at evaluation time. If you `window.eval(...)` them **inside an async function**, those internal timers never fire and subsequent `um.init()` calls silently do nothing. The jsdom setup **must run from synchronous module-top code** — which is why `src/index.ts` has an `initAtRequireTime()` IIFE that evaluates AWSC during the `require()` call itself.

Secondary wrinkle: even with that, the first `fetch()` fired from inside `NodeXHR.prototype.send` fails with "fetch failed" unless we yield the event loop once (`await setImmediate()` + `await setTimeout(1500)`) before the first `um.init()`. `src/lib/tokens.ts` handles this.

---

## Production checklist

- [ ] **Run `npm run bundle`** to inline the AWSC scripts into `dist/` — makes your Docker image self-contained.
- [ ] **Put the server behind a gateway with auth** — `server.js` has no auth; anyone with network access can burn your guest quota.
- [ ] **Mount `~/.cache/qwen-node` on a writable volume** (or set `QWEN_CACHE_DIR`). Saves ~10s per restart.
- [ ] **Set `HTTPS_PROXY`** if your cluster has an egress proxy; `proxy-setup.ts` picks it up automatically.
- [ ] **Monitor for `RateLimited`** — if `retryAfterHours > 0` consistently, rotation isn't helping and you need a different egress IP.
- [ ] **Read Qwen's ToS before shipping.** Guest endpoints aren't meant for programmatic access. Consider a real API key for production.
- [ ] **Pin AWSC versions** (see `AWSC_URLS` in `src/lib/constants.ts`). If Alibaba ships an update that changes the token format, regenerate the bundle and re-pin.
