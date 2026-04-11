// src/lib/http.js
// Low-level Qwen API calls. Nothing here knows about OpenAI-style
// messages, session caching, or multi-turn state — those live one level up.
//
// Exports:
//   baseHeaders(session, extra?)           — assembles the 13 headers every
//                                             request needs (bx-ua, bx-umid,
//                                             Cookie, etc.) from a Session.
//   createChat(session, opts?)             — POST /api/v2/chats/new → chat_id
//   streamChatCompletion(session, chatId,  — async iterator that yields
//                        prompt, opts?)      structured events from the SSE.
'use strict';

const crypto = require('node:crypto');
const { BASE, API, SPA_VERSION, DEFAULT_MODEL } = require('./constants');
const { parseSSEBlock } = require('./sse');

function baseHeaders(session, extra = {}) {
  return {
    Accept: 'application/json, text/plain, */*',
    'Accept-Language': 'en-US',
    'Content-Type': 'application/json',
    Origin: BASE,
    Referer: BASE + '/',
    'User-Agent': session.userAgent,
    Version: SPA_VERSION,
    // The Qwen frontend puts `source: web` on every request, and its own
    // request interceptor explicitly strips any Authorization header when
    // source=web. That's how guest auth works: it's 100% cookies + bx-*.
    source: 'web',
    'X-Request-Id': crypto.randomUUID(),
    'bx-ua': session.bxUa,
    'bx-umidtoken': session.bxUmidtoken,
    'bx-v': session.bxV,
    Cookie: session.cookieHeader,
    ...extra,
  };
}

async function createChat(session, {
  model = DEFAULT_MODEL,
  chatMode = 'normal',
  chatType = 't2t',
} = {}) {
  const res = await fetch(`${API}/chats/new`, {
    method: 'POST',
    headers: baseHeaders(session),
    body: JSON.stringify({
      title: 'New Chat',
      models: [model],
      chat_mode: chatMode,
      chat_type: chatType,
      // NOTE: chats/new uses milliseconds; chat/completions uses seconds.
      // Yes, it's inconsistent on Qwen's side. Don't try to unify it.
      timestamp: Date.now(),
    }),
  });
  const json = await res.json().catch(() => null);
  if (!res.ok || !json || json.success !== true) {
    const err = new Error(`createChat failed HTTP ${res.status}: ${JSON.stringify(json)}`);
    err.status = res.status;
    err.response = json;
    throw err;
  }
  return json.data.id;
}

// Async generator over the SSE stream. Yields events of the following shapes:
//
//   { type: 'created',   chatId, parentId, responseId }
//   { type: 'info',      info: <response.info payload> }   // keep-alive etc.
//   { type: 'delta',     content, fullContent, phase, role, usage,
//                        functionCall, functionId, extra }
//   { type: 'tool',      phase, role, functionCall, functionId, name, extra }
//   { type: 'finished',  fullContent, responseId }
//
// Errors returned as HTTP 200 + application/json (RateLimited, Unauthorized,
// Bad_Request, Internal_Server_Error) are thrown as structured Error objects
// with .code / .status / .retryAfterHours / .response fields.
async function* streamChatCompletion(session, chatId, prompt, {
  model = DEFAULT_MODEL,
  chatMode = 'normal',
  chatType = 't2t',
  parentId = null,
  thinkingEnabled = false,
} = {}) {
  const body = {
    stream: true,
    version: '2.1',
    incremental_output: true,
    chat_id: chatId,
    chat_mode: chatMode,
    model,
    parent_id: parentId,
    // Qwen rejects messages.length > 1 with "Invalid input too many messages."
    // History is tracked server-side via chat_id + parent_id.
    messages: [
      {
        role: 'user',
        content: prompt,
        chat_type: chatType,
        feature_config: { thinking_enabled: thinkingEnabled, output_schema: 'phase' },
        extra: {},
        sub_chat_type: chatType,
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
    const err = new Error(`chat/completions HTTP ${res.status}: ${t.slice(0, 300)}`);
    err.status = res.status;
    throw err;
  }

  // Error responses come back as 200 OK with application/json — NOT as SSE.
  const ct = res.headers.get('content-type') || '';
  if (!ct.includes('text/event-stream')) {
    const text = await res.text().catch(() => '');
    let parsed = null;
    try { parsed = JSON.parse(text); } catch (_) {}
    const code = parsed && parsed.data && parsed.data.code;
    const details = (parsed && parsed.data && (parsed.data.details || parsed.data.template))
      || text.slice(0, 300);
    const err = new Error(`streamChatCompletion non-SSE (${code || 'unknown'}): ${details}`);
    err.code = code;
    err.retryAfterHours = (parsed && parsed.data && parsed.data.num) || null;
    err.response = parsed || text;
    throw err;
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder('utf-8');
  let buffer = '';
  let fullContent = '';
  let responseId = null;

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
        responseId = payload['response.created'].response_id;
        yield {
          type: 'created',
          chatId: payload['response.created'].chat_id,
          parentId: payload['response.created'].parent_id,
          responseId,
        };
        continue;
      }
      if (payload['response.info']) {
        yield { type: 'info', info: payload['response.info'] };
        continue;
      }

      const choice = payload.choices && payload.choices[0];
      if (!choice || !choice.delta) continue;
      const delta = choice.delta;

      // Tool-call / tool-result event. We emit these FIRST because the
      // tool_result frame arrives with `status: "finished"` and we must
      // not swallow it as the turn's terminal event below.
      if (delta.function_call || (delta.extra && delta.extra.tool_result)) {
        yield {
          type: 'tool',
          phase: delta.phase,
          role: delta.role,
          functionCall: delta.function_call,
          functionId: delta.function_id,
          name: delta.name,
          extra: delta.extra,
        };
        // Tool results usually carry no text content, but don't `continue`
        // just in case future frames combine both. Fall through.
      }

      if (delta.status === 'finished') {
        yield { type: 'finished', fullContent, responseId: payload.response_id || responseId };
        continue;
      }

      // Typed content delta (usually phase: "answer" or "think")
      if (typeof delta.content === 'string' && delta.content.length) {
        fullContent += delta.content;
        yield {
          type: 'delta',
          content: delta.content,
          fullContent,
          phase: delta.phase,
          role: delta.role,
          usage: payload.usage,
          functionCall: delta.function_call,
          functionId: delta.function_id,
          extra: delta.extra,
        };
      }
    }
  }
}

module.exports = { baseHeaders, createChat, streamChatCompletion };
