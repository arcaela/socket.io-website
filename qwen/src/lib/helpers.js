// src/lib/helpers.js
// The user-facing API. `chat()` is the primitive — every other helper is a
// thin wrapper that creates a throwaway `chat()` and calls one of its turn
// methods. This means credentials + session handling live in exactly one
// place and the top-level shortcuts can't drift from the stateful ones.
//
// Primary:
//   chat(opts?)                  → stateful multi-turn handle
//                                  { chatId, model, turn, history, lastResponseId,
//                                    ask, search, image, think }
//
// One-shot wrappers (each creates a fresh chat and invokes the same method):
//   ask(prompt, opts?)           → string                — via chat(opts).ask(prompt)
//   search(query, opts?)         → { reply, sources, usage }   — via chat(opts).search(query)
//   image(prompt, opts?)         → { url, width, height, model } — via chat(opts).image(prompt)
//   think(prompt, opts?)         → { reply, thinking, usage }   — via chat(opts).think(prompt)
//
// The relationship is: the four top-level methods share the exact same
// implementation that the four chat methods use. There's no duplicated
// parsing / extraction / credential code.
'use strict';

const { DEFAULT_MODEL } = require('./constants');

// ---- shape extractors ----
//
// Used by both chat methods and one-shot helpers to hide Qwen's raw
// delta/tool_result plumbing behind clean result objects.

// Pulls deduped { url, title, snippet, date, hostname } records out of every
// web_search tool_result event captured during the turn.
function extractSources(sendResult) {
  const sources = [];
  if (!sendResult || !sendResult.toolEvents) return sources;
  for (const ev of sendResult.toolEvents) {
    const docs = ev.extra && ev.extra.tool_result && ev.extra.tool_result.docs;
    if (!docs || !Array.isArray(docs)) continue;
    for (const doc of docs) {
      if (!doc || !doc.url) continue;
      if (sources.some((s) => s.url === doc.url)) continue;
      sources.push({
        url: doc.url,
        title: doc.title || null,
        snippet: doc.snippet || null,
        date: doc.date || null,
        hostname: doc.hostname || null,
      });
    }
  }
  return sources;
}

// For t2i: Qwen returns the CDN URL as the plain text content of the
// image_gen phase delta, and the dimensions in the `usage` object.
function extractImage(sendResult, model) {
  const url = (sendResult && sendResult.reply ? sendResult.reply : '').trim();
  const usage = (sendResult && sendResult.usage) || {};
  return {
    url,
    width: usage.width || null,
    height: usage.height || null,
    model: model || null,
  };
}

// ---- factory ----
//
// Receives a reference to the lower-level `conversation(opts)` via DI so
// this module has no import cycle with src/index.js.
function makeHelpers({ conversation }) {

  // --- PRIMITIVE: chat() ---
  //
  // A stateful multi-turn conversation. All four one-shot helpers below
  // delegate to this.
  async function chat(opts = {}) {
    const {
      model = DEFAULT_MODEL,
      system = null,
      chatType = 't2t',
      chatMode = 'normal',
      thinking = false,
    } = opts;

    const conv = await conversation({ model, system, chatType, chatMode, thinking });

    const handle = {
      // Identity
      chatId: conv.chatId,
      model: conv.model,

      // Live state
      get turn() { return conv.turn; },
      get history() { return conv.history; },
      get lastResponseId() { return conv.lastResponseId; },

      // === Turn methods ===

      // Plain text turn. Uses the chat's default chatType (normally 't2t').
      async ask(message, sendOpts = {}) {
        const r = await conv.send(message, sendOpts);
        return {
          reply: r.reply,
          turn: r.turn,
          usage: r.usage,
          thinking: r.thinking || null,
        };
      },

      // Web search turn. Forces chatType='search' on this turn only so the
      // model invokes the built-in web_search tool. Returns the structured
      // sources list alongside the text reply.
      async search(query, sendOpts = {}) {
        const r = await conv.send(query, Object.assign({}, sendOpts, { chatType: 'search' }));
        return {
          reply: r.reply,
          sources: extractSources(r),
          turn: r.turn,
          usage: r.usage,
        };
      },

      // Image generation turn. Forces chatType='t2i' and returns the signed
      // CDN URL along with its dimensions.
      async image(prompt, sendOpts = {}) {
        const r = await conv.send(prompt, Object.assign({}, sendOpts, { chatType: 't2i' }));
        return extractImage(r, conv.model);
      },

      // Thinking turn. Forces thinking_enabled=true on this turn so the
      // model emits its chain-of-thought in a separate phase. Returns both
      // the thinking text and the final answer.
      async think(message, sendOpts = {}) {
        const r = await conv.send(message, Object.assign({}, sendOpts, { thinking: true }));
        return {
          reply: r.reply,
          thinking: r.thinking,
          turn: r.turn,
          usage: r.usage,
        };
      },
    };

    return handle;
  }

  // --- ONE-SHOT WRAPPERS ---
  //
  // Each of these is literally `create a throwaway chat and call the
  // matching method on it`. No duplicated logic — the chat IS the primitive.

  // Returns a bare string (not an object) for ergonomic one-line use.
  async function ask(prompt, opts = {}) {
    const ch = await chat(opts);
    const { reply } = await ch.ask(prompt, { onDelta: opts.onDelta });
    return reply;
  }

  async function search(query, opts = {}) {
    const ch = await chat(opts);
    return ch.search(query, { onDelta: opts.onDelta });
  }

  async function image(prompt, opts = {}) {
    const ch = await chat(opts);
    return ch.image(prompt);
  }

  async function think(prompt, opts = {}) {
    const ch = await chat(opts);
    return ch.think(prompt, { onDelta: opts.onDelta });
  }

  return { chat, ask, search, image, think };
}

module.exports = { makeHelpers, extractSources, extractImage };
