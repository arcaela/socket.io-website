// src/index.js
// Public entry point. Exports a single callable `qwen(messages, opts)`
// with `.stream`, `.warmup`, `.conversation`, `.getSession` attached.
//
// Usage:
//   const qwen = require('./src');
//   const reply = await qwen([{ role: 'user', content: 'hola' }]);
//   for await (const ev of qwen.stream('count to 5')) { ... }
//   const session = await qwen.warmup();
//   const conv = await qwen.conversation({ system: '...', chatType: 'search' });
//
// ⚠️ REQUIRE-TIME SIDE EFFECTS
// This module eagerly wires undici's proxy and — if there's no valid cached
// session on disk — immediately builds the jsdom environment and evaluates
// Alibaba's AWSC scripts. The ordering matters: AWSC's internal timers only
// fire correctly when the eval happens from synchronous module-level code,
// not from inside an async function. See README.md § "The ordering quirk"
// for the full explanation.
'use strict';

// 1) Proxy side-effect (safe no-op if no env vars)
require('./lib/proxy-setup');

const { DEFAULT_MODEL, BASE, SESSION_TTL_MS } = require('./lib/constants');
const { getAwscScripts } = require('./lib/awsc-scripts');
const { createJsdomEnv } = require('./lib/jsdom-env');
const { loadCachedSession, makeGetSession } = require('./lib/session');
const { createChat, streamChatCompletion } = require('./lib/http');
const { flattenMessages } = require('./lib/messages');
const { makeConversation } = require('./lib/conversation');
const { makeHelpers } = require('./lib/helpers');
const errors = require('./lib/errors');

// 2) Lazy jsdom env — built at most once per process. We expose this via a
//    closure so session.js can request it only when it actually needs to
//    generate fresh tokens. Crucially the FIRST call must happen while we're
//    still at module-top-level (below).
let _jsdomEnv = null;
function ensureJsdomEnv() {
  if (_jsdomEnv) return _jsdomEnv;
  const scripts = getAwscScripts();
  if (!scripts) {
    throw new Error(
      'qwen: could not obtain AWSC scripts. Is the network reachable, ' +
      'or is the bundle embedded EMBEDDED_AWSC constant missing?'
    );
  }
  _jsdomEnv = createJsdomEnv(scripts);
  return _jsdomEnv;
}

// 3) Module-level kickoff. This is the core workaround: if the disk cache
//    doesn't hold a valid session, we build the jsdom env RIGHT NOW, during
//    require(). Skipping it when a cache exists keeps warm starts fast.
(function initAtRequireTime() {
  try {
    if (loadCachedSession()) return; // warm path
  } catch (_) {}
  try { ensureJsdomEnv(); }
  catch (e) {
    // Non-fatal: first qwen() call will retry. Useful when a pod boots
    // offline or before the proxy is ready.
    if (process.env.QWEN_DEBUG) console.error('[qwen] require-time init failed:', e.message);
  }
})();

// 4) Build the high-level helpers that depend on the jsdom env.
const getSession = makeGetSession({ ensureJsdomEnv });
const conversation = makeConversation({ getSession });
const helpers = makeHelpers({ conversation });

// 5) The callable default export.
async function qwen(messages, options = {}) {
  const {
    model = DEFAULT_MODEL,
    system = null,
    chatType = 't2t',
    chatMode = 'normal',
    thinkingEnabled = false,
    onDelta = null,
    retryOnAuthFail = true,
  } = options;

  if (typeof messages === 'string') messages = [{ role: 'user', content: messages }];
  if (system) messages = [{ role: 'system', content: system }, ...messages];
  const prompt = flattenMessages(messages);

  async function attempt({ forceRefresh }) {
    const session = await getSession({ forceRefresh });
    const chatId = await createChat(session, { model, chatMode, chatType });
    let full = '';
    for await (const ev of streamChatCompletion(session, chatId, prompt, {
      model, chatMode, chatType, thinkingEnabled,
    })) {
      if (ev.type === 'delta') {
        full = ev.fullContent;
        if (onDelta) onDelta(ev);
      }
    }
    return full;
  }

  try {
    return await attempt({ forceRefresh: false });
  } catch (e) {
    // Auto-rotate session on auth / rate-limit errors. Each forceRefresh
    // performs a fresh um.init and gets a brand-new bx-umidtoken — which
    // is tracked per-device by Qwen, so it resets the guest quota.
    const rotate =
      retryOnAuthFail && (
        e instanceof errors.QwenRateLimitedError ||
        e instanceof errors.QwenUnauthorizedError
      );
    if (rotate) {
      ensureJsdomEnv();
      return attempt({ forceRefresh: true });
    }
    throw e;
  }
}

// Legacy OpenAI-style streaming. Kept for compatibility with callers that
// speak OpenAI's chat-completions shape. For the intent-specific typed
// streams, use `qwen.ask.stream`, `qwen.search.stream`, `qwen.image.stream`,
// `qwen.think.stream` (or the chat versions) instead.
qwen.stream = async function* (messages, options = {}) {
  const {
    model = DEFAULT_MODEL,
    system = null,
    chatType = 't2t',
    chatMode = 'normal',
    thinkingEnabled = false,
  } = options;
  if (typeof messages === 'string') messages = [{ role: 'user', content: messages }];
  if (system) messages = [{ role: 'system', content: system }, ...messages];
  const prompt = flattenMessages(messages);
  const session = await getSession();
  const chatId = await createChat(session, { model, chatMode, chatType });
  yield* streamChatCompletion(session, chatId, prompt, {
    model, chatMode, chatType, thinkingEnabled,
  });
};

// 7) Explicit startup warmup — ideal as the first thing a container's
//    bootstrap code does. Returns the current session metadata.
qwen.warmup = async function ({ forceRefresh = false } = {}) {
  if (forceRefresh) ensureJsdomEnv();
  const session = await getSession({ forceRefresh });
  return {
    ok: true,
    createdAt: session.createdAt,
    expiresIn: SESSION_TTL_MS - (Date.now() - session.createdAt),
  };
};

// -------- High-level helpers (the practical API) --------
//
//   qwen.ask(prompt, opts?)           → string
//   qwen.search(query, opts?)         → { reply, sources, usage }
//   qwen.image(prompt, opts?)         → { url, width, height, model }
//   qwen.think(prompt, opts?)         → { reply, thinking, usage }
//   qwen.chat(opts?)                  → stateful multi-turn handle
//
// See src/lib/helpers.js for the full signatures.
qwen.ask = helpers.ask;
qwen.search = helpers.search;
qwen.image = helpers.image;
qwen.think = helpers.think;
qwen.chat = helpers.chat;

// -------- Error classes --------
//
// Callers can use `instanceof` or check `.code`:
//   try { await qwen.ask('...') }
//   catch (e) {
//     if (e instanceof qwen.errors.QwenRateLimitedError) { ... }
//     if (e.code === 'RateLimited') { ... }
//   }
qwen.errors = errors;
qwen.QwenError = errors.QwenError;
qwen.QwenRateLimitedError = errors.QwenRateLimitedError;
qwen.QwenUnauthorizedError = errors.QwenUnauthorizedError;
qwen.QwenBadRequestError = errors.QwenBadRequestError;
qwen.QwenServerError = errors.QwenServerError;
qwen.QwenNetworkError = errors.QwenNetworkError;

// -------- Lower-level API (still exposed) --------
qwen.conversation = conversation;
qwen.getSession = getSession;
qwen.DEFAULT_MODEL = DEFAULT_MODEL;
qwen.BASE = BASE;

module.exports = qwen;
