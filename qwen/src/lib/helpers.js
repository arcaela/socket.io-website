// src/lib/helpers.js
// The practical API. Every method is exposed in two forms:
//
//   method(input, opts?)         → Promise<result>         (collected)
//   method.stream(input, opts?)  → AsyncIterable<Event>    (streaming)
//
// Both forms go through `qwen.conversation()` under the hood — specifically
// `conv.send()` for the collected form and `conv.stream()` for the iterator
// form. There's no duplicated parsing or session logic.
//
// `qwen.chat()` is the primitive. The four top-level one-shot helpers
// (ask, search, image, think) are one-line wrappers that create a throwaway
// chat and invoke the matching method on it.
'use strict';

const { DEFAULT_MODEL } = require('./constants');

// -------- low-level: pump an iterator and collect typed events --------
//
// These helpers walk a `conv.stream()` generator and pull out the final
// done event, which carries the aggregated result.
async function _consumeDone(stream) {
  let done = null;
  for await (const ev of stream) {
    if (ev.type === 'done') done = ev;
  }
  if (!done) throw new Error('stream ended without done event');
  return done;
}

// -------- shape extractors for top-level return values --------

function _toAskResult(done) {
  return {
    reply: done.reply,
    turn: done.turn,
    usage: done.usage,
    thinking: done.thinking || null,
  };
}

function _toSearchResult(done) {
  return {
    reply: done.reply,
    sources: done.sources || [],
    turn: done.turn,
    usage: done.usage,
  };
}

function _toImageResult(done, model) {
  const img = done.image || {};
  return {
    url: img.url || (done.reply || '').trim(),
    width: img.width || null,
    height: img.height || null,
    model: model || null,
    turn: done.turn,
  };
}

function _toThinkResult(done) {
  return {
    reply: done.reply,
    thinking: done.thinking || null,
    turn: done.turn,
    usage: done.usage,
  };
}

// -------- factory --------

function makeHelpers({ conversation }) {

  // === PRIMITIVE: chat() ===
  //
  // Returns a stateful handle with four turn methods, each with a .stream
  // property that exposes the same underlying conv.stream() iterator.
  async function chat(opts = {}) {
    const {
      model = DEFAULT_MODEL,
      system = null,
      chatType = 't2t',
      chatMode = 'normal',
      thinking = false,
    } = opts;

    const conv = await conversation({ model, system, chatType, chatMode, thinking });

    // -- chat.ask --------------------------------------------------------
    async function ask(message, sendOpts = {}) {
      const done = await _consumeDone(conv.stream(message, sendOpts));
      return _toAskResult(done);
    }
    ask.stream = function askStream(message, sendOpts = {}) {
      return conv.stream(message, sendOpts);
    };

    // -- chat.search -----------------------------------------------------
    async function search(query, sendOpts = {}) {
      const done = await _consumeDone(conv.stream(
        query, Object.assign({}, sendOpts, { chatType: 'search' })
      ));
      return _toSearchResult(done);
    }
    search.stream = function searchStream(query, sendOpts = {}) {
      return conv.stream(query, Object.assign({}, sendOpts, { chatType: 'search' }));
    };

    // -- chat.image ------------------------------------------------------
    async function image(prompt, sendOpts = {}) {
      const done = await _consumeDone(conv.stream(
        prompt, Object.assign({}, sendOpts, { chatType: 't2i' })
      ));
      return _toImageResult(done, conv.model);
    }
    image.stream = function imageStream(prompt, sendOpts = {}) {
      return conv.stream(prompt, Object.assign({}, sendOpts, { chatType: 't2i' }));
    };

    // -- chat.think ------------------------------------------------------
    async function think(message, sendOpts = {}) {
      const done = await _consumeDone(conv.stream(
        message, Object.assign({}, sendOpts, { thinking: true })
      ));
      return _toThinkResult(done);
    }
    think.stream = function thinkStream(message, sendOpts = {}) {
      return conv.stream(message, Object.assign({}, sendOpts, { thinking: true }));
    };

    return {
      chatId: conv.chatId,
      model: conv.model,
      get turn() { return conv.turn; },
      get history() { return conv.history; },
      get lastResponseId() { return conv.lastResponseId; },
      ask,
      search,
      image,
      think,
    };
  }

  // === ONE-SHOT WRAPPERS ===
  //
  // Each creates a throwaway chat and calls the matching method on it.
  // The .stream variant mirrors the same code path over the streaming form.

  async function ask(prompt, opts = {}) {
    const ch = await chat(opts);
    const { reply } = await ch.ask(prompt);
    return reply;
  }
  ask.stream = async function* askStream(prompt, opts = {}) {
    const ch = await chat(opts);
    yield* ch.ask.stream(prompt);
  };

  async function search(query, opts = {}) {
    const ch = await chat(opts);
    const r = await ch.search(query);
    return { reply: r.reply, sources: r.sources, usage: r.usage };
  }
  search.stream = async function* searchStream(query, opts = {}) {
    const ch = await chat(opts);
    yield* ch.search.stream(query);
  };

  async function image(prompt, opts = {}) {
    const ch = await chat(opts);
    const r = await ch.image(prompt);
    return { url: r.url, width: r.width, height: r.height, model: r.model };
  }
  image.stream = async function* imageStream(prompt, opts = {}) {
    const ch = await chat(opts);
    yield* ch.image.stream(prompt);
  };

  async function think(prompt, opts = {}) {
    const ch = await chat(opts);
    const r = await ch.think(prompt);
    return { reply: r.reply, thinking: r.thinking, usage: r.usage };
  }
  think.stream = async function* thinkStream(prompt, opts = {}) {
    const ch = await chat(opts);
    yield* ch.think.stream(prompt);
  };

  return { chat, ask, search, image, think };
}

module.exports = { makeHelpers };
