// src/lib/conversation.js
// Stateful multi-turn conversation handle. Wraps a single chat_id and
// chains parent_id across turns so the backend tracks history server-side.
//
//   const conv = await qwen.conversation({ system: '...', chatType: 'search' });
//   const { reply } = await conv.send('Hello');
//   const { reply } = await conv.send('and now in French');
//   console.log(conv.turn, conv.history);
//
// Each `send` returns an object with reply/thinking/usage/toolEvents/turn.
// `toolEvents` is a list of raw tool-call records captured during the turn,
// useful for harvesting web_search citations or image-gen URLs without
// re-parsing the delta stream.
'use strict';

const { API, DEFAULT_MODEL } = require('./constants');
const { baseHeaders, createChat } = require('./http');
const { parseSSEBlock } = require('./sse');

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
    const chatId = await createChat(session, { model, chatMode, chatType });

    // Mutable per-conversation state
    let lastResponseId = null;
    let turnCount = 0;
    const history = [];

    async function send(message, sendOpts = {}) {
      const useChatType = sendOpts.chatType || chatType;
      const useChatMode = sendOpts.chatMode || chatMode;
      const useThinking = sendOpts.thinking != null ? sendOpts.thinking : thinking;
      const onDelta = sendOpts.onDelta || null;

      // Inject the system prompt ONLY on the first turn — the backend
      // remembers the whole thread under chat_id so repeating it wastes
      // tokens.
      let prompt = message;
      if (system && turnCount === 0) {
        prompt = `[Instrucciones del sistema]\n${system}\n\n[Mensaje del usuario]\n${message}`;
      }

      const body = {
        stream: true,
        version: '2.1',
        incremental_output: true,
        chat_id: chatId,
        chat_mode: useChatMode,
        model,
        parent_id: lastResponseId,
        messages: [
          {
            role: 'user',
            content: prompt,
            chat_type: useChatType,
            feature_config: {
              thinking_enabled: useThinking,
              output_schema: 'phase',
            },
            extra: {},
            sub_chat_type: useChatType,
          },
        ],
        timestamp: Math.floor(Date.now() / 1000),
      };

      const res = await fetch(
        `${API}/chat/completions?chat_id=${encodeURIComponent(chatId)}`,
        {
          method: 'POST',
          headers: baseHeaders(session, {
            Accept: 'text/event-stream',
            'X-Accel-Buffering': 'no',
          }),
          body: JSON.stringify(body),
        }
      );

      if (!res.ok) {
        const t = await res.text().catch(() => '');
        const err = new Error(`conversation.send HTTP ${res.status}: ${t.slice(0, 400)}`);
        err.status = res.status;
        throw err;
      }

      // Rate-limit / Unauthorized / Bad_Request arrive as HTTP 200 + JSON.
      const ct = res.headers.get('content-type') || '';
      if (!ct.includes('text/event-stream')) {
        const text = await res.text().catch(() => '');
        let parsed = null;
        try { parsed = JSON.parse(text); } catch (_) {}
        const code = parsed && parsed.data && parsed.data.code;
        const details = (parsed && parsed.data && (parsed.data.details || parsed.data.template))
          || text.slice(0, 300);
        const err = new Error(`conversation.send non-SSE (${code || 'unknown'}): ${details}`);
        err.code = code;
        err.retryAfterHours = (parsed && parsed.data && parsed.data.num) || null;
        err.response = parsed || text;
        throw err;
      }

      // Parse the SSE stream and accumulate the answer / thinking text.
      const reader = res.body.getReader();
      const decoder = new TextDecoder('utf-8');
      let buffer = '';
      let answerText = '';
      let thinkingText = '';
      let usage = null;
      let currentResponseId = null;
      const toolEvents = [];

      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        while (true) {
          const sep = buffer.indexOf('\n\n');
          if (sep === -1) break;
          const block = buffer.slice(0, sep);
          buffer = buffer.slice(sep + 2);
          const payload = parseSSEBlock(block);
          if (!payload) continue;
          if (payload['response.created']) {
            currentResponseId = payload['response.created'].response_id;
            continue;
          }
          const choice = payload.choices && payload.choices[0];
          if (!choice || !choice.delta) continue;
          const delta = choice.delta;
          if (payload.usage) usage = payload.usage;
          if (delta.status === 'finished') continue;

          // Record tool-related events (web_search, image_gen, etc.)
          if (delta.function_call || (delta.extra && delta.extra.tool_result)) {
            toolEvents.push({
              phase: delta.phase,
              functionCall: delta.function_call,
              functionId: delta.function_id,
              name: delta.name,
              extra: delta.extra,
            });
          }

          if (typeof delta.content === 'string' && delta.content.length) {
            if (delta.phase === 'think') thinkingText += delta.content;
            else answerText += delta.content;
            if (onDelta) onDelta(Object.assign({}, delta, { fullContent: answerText }));
          }
        }
      }

      if (currentResponseId) lastResponseId = currentResponseId;
      turnCount += 1;
      history.push({ role: 'user', content: message });
      history.push({
        role: 'assistant',
        content: answerText,
        thinking: thinkingText || undefined,
      });

      return {
        reply: answerText,
        thinking: thinkingText || null,
        responseId: currentResponseId,
        parentId: lastResponseId,
        turn: turnCount,
        usage,
        toolEvents,
      };
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
    };
  };
}

module.exports = { makeConversation };
