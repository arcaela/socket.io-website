// src/lib/conversation.js
// Stateful multi-turn conversation handle. Wraps a single `chat_id` and
// chains `parent_id` across turns so Qwen's backend tracks history
// server-side.
//
// Exposes TWO forms per turn:
//
//   conv.send(message, opts?)       → Promise<result>
//       Collects the entire response and returns an object with everything
//       the caller needs: reply, thinking, sources, usage, etc.
//
//   conv.stream(message, opts?)     → AsyncIterable<TypedEvent>
//       Yields discrete, discriminated-union events as the server streams.
//       Always ends with a { type: 'done', ... } event carrying the same
//       aggregated data that .send() would have returned. Errors throw
//       during iteration — never arrive as chunks.
//
// Both forms go through the same underlying _sendStream() generator so
// they share the same state mutation (lastResponseId, turn counter, history).
'use strict';

const { API, DEFAULT_MODEL } = require('./constants');
const { baseHeaders, streamChatCompletion } = require('./http');
const { QwenError } = require('./errors');

// -------- source extractor (shared with helpers) --------

// Pulls { url, title, snippet, date, hostname } records out of a tool
// event that carries a web_search tool_result. Deduped by URL.
function extractSourcesFromToolEvent(ev) {
  const out = [];
  const docs = ev && ev.extra && ev.extra.tool_result && ev.extra.tool_result.docs;
  if (!docs || !Array.isArray(docs)) return out;
  for (const doc of docs) {
    if (!doc || !doc.url) continue;
    out.push({
      url: doc.url,
      title: doc.title || null,
      snippet: doc.snippet || null,
      date: doc.date || null,
      hostname: doc.hostname || null,
    });
  }
  return out;
}

function makeConversation({ getSession }) {
  return async function conversation(opts = {}) {
    const {
      model = DEFAULT_MODEL,
      chatMode = 'normal',
      chatType = 't2t',
      system = null,
      thinking = false,
    } = opts;

    const session = await getSession();
    const { createChat } = require('./http');
    const chatId = await createChat(session, { model, chatMode, chatType });

    // Mutable per-conversation state
    let lastResponseId = null;
    let turnCount = 0;
    const history = [];

    // The canonical send path. Returns an async generator that emits
    // typed events and, as its FINAL event, a `done` with the aggregated
    // result. All state mutation (turn++, history.push, lastResponseId)
    // happens right before the done event is emitted.
    async function* _sendStream(message, sendOpts = {}) {
      const useChatType = sendOpts.chatType || chatType;
      const useChatMode = sendOpts.chatMode || chatMode;
      const useThinking = sendOpts.thinking != null ? sendOpts.thinking : thinking;

      // System prompt is injected ONLY on the first turn — the backend
      // remembers it afterwards via chat_id, so repeating wastes tokens.
      let prompt = message;
      if (system && turnCount === 0) {
        prompt = `[Instrucciones del sistema]\n${system}\n\n[Mensaje del usuario]\n${message}`;
      }

      // Accumulators — these fill up as the lower-level iterator yields.
      let answerText = '';
      let thinkingText = '';
      let usage = null;
      let responseId = null;
      const sources = [];
      const toolEvents = [];
      let imageMeta = null;

      for await (const ev of streamChatCompletion(session, chatId, prompt, {
        model,
        chatMode: useChatMode,
        chatType: useChatType,
        parentId: lastResponseId,
        thinkingEnabled: useThinking,
      })) {
        if (ev.type === 'created') {
          responseId = ev.responseId;
          yield {
            type: 'start',
            chatId: ev.chatId,
            responseId: ev.responseId,
            parentId: ev.parentId,
          };
          continue;
        }

        if (ev.type === 'info') {
          // keep_alive and similar side-channel metadata
          yield { type: 'info', info: ev.info };
          continue;
        }

        if (ev.type === 'tool') {
          toolEvents.push(ev);

          // A tool call arrives in two waves: first the function_call args
          // stream in, then a tool_result arrives with the output.
          if (ev.functionCall) {
            yield {
              type: 'tool_call',
              name: ev.functionCall.name,
              arguments: ev.functionCall.arguments || '',
              phase: ev.phase,
              functionId: ev.functionId,
            };
          }

          const docs = ev.extra && ev.extra.tool_result && ev.extra.tool_result.docs;
          if (docs && Array.isArray(docs)) {
            const newSources = extractSourcesFromToolEvent(ev);
            for (const s of newSources) {
              if (!sources.some((x) => x.url === s.url)) sources.push(s);
            }
            yield { type: 'sources', sources: newSources };
          }
          continue;
        }

        if (ev.type === 'delta') {
          if (ev.usage) usage = ev.usage;

          if (ev.phase === 'think') {
            thinkingText += ev.content;
            yield {
              type: 'thinking',
              content: ev.content,
              fullThinking: thinkingText,
            };
            continue;
          }

          if (ev.phase === 'image_gen') {
            // The `content` field IS the signed CDN URL. Dimensions come
            // from the same delta's usage object (width/height/image_count).
            imageMeta = {
              url: (ev.content || '').trim(),
              width: (ev.usage && ev.usage.width) || null,
              height: (ev.usage && ev.usage.height) || null,
            };
            yield {
              type: 'image',
              url: imageMeta.url,
              width: imageMeta.width,
              height: imageMeta.height,
              extra: ev.extra || null,
            };
            continue;
          }

          // Default: phase === 'answer' or similar text phase
          answerText += ev.content;
          yield {
            type: 'text',
            content: ev.content,
            fullContent: answerText,
            phase: ev.phase,
          };
          continue;
        }

        if (ev.type === 'finished') {
          // Don't yield anything here — the `done` event below carries the
          // same information plus the collected result.
          if (ev.responseId) responseId = ev.responseId;
          continue;
        }
      }

      // -- state mutation + done event --
      if (responseId) lastResponseId = responseId;
      turnCount += 1;
      history.push({ role: 'user', content: message });
      history.push({
        role: 'assistant',
        content: answerText,
        thinking: thinkingText || undefined,
      });

      yield {
        type: 'done',
        reply: answerText,
        thinking: thinkingText || null,
        sources: sources.length ? sources : null,
        image: imageMeta,
        responseId,
        parentId: lastResponseId,
        turn: turnCount,
        usage,
        toolEvents,
      };
    }

    // Public iterator form. Note: consumers who want the collected result
    // should either use .send() or look for the final `done` event.
    function stream(message, sendOpts = {}) {
      return _sendStream(message, sendOpts);
    }

    // Public collected form. Runs the iterator to completion and returns
    // the payload of the terminal `done` event.
    async function send(message, sendOpts = {}) {
      let done = null;
      for await (const ev of _sendStream(message, sendOpts)) {
        if (ev.type === 'done') done = ev;
      }
      if (!done) {
        throw new QwenError('conversation.send: stream ended without a done event');
      }
      return done;
    }

    return {
      chatId,
      model,
      chatMode,
      chatType,
      get turn() { return turnCount; },
      get history() { return history.slice(); },
      get lastResponseId() { return lastResponseId; },
      send,
      stream,
    };
  };
}

module.exports = { makeConversation, extractSourcesFromToolEvent };
