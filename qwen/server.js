#!/usr/bin/env node
// server.js — tiny HTTP front for the compiled Qwen class. Main process
// for a Docker container.
//
// Endpoints:
//   GET    /healthz
//   POST   /v1/chat/completions              (stateless, OpenAI-ish)
//   POST   /v1/conversations                  (create a server-side chat)
//   POST   /v1/conversations/:id/messages     (send a turn)
//   DELETE /v1/conversations/:id              (drop from memory)
'use strict';

const http = require('node:http');
const crypto = require('node:crypto');
const Qwen = require('./dist');  // main = dist/index.js

const PORT = parseInt(process.env.PORT || '8787', 10);
const REFRESH_ON_429 = process.env.QWEN_REFRESH_ON_429 !== '0';

// In-memory conversation store. Replace with Redis/DB in production.
const chats = new Map();
function newId() { return crypto.randomUUID(); }

function readJson(req) {
  return new Promise((resolve, reject) => {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
      try { resolve(body ? JSON.parse(body) : {}); }
      catch { reject(new Error('invalid JSON body')); }
    });
    req.on('error', reject);
  });
}
function writeJson(res, status, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Content-Length': Buffer.byteLength(body),
  });
  res.end(body);
}
function writeSseStart(res) {
  res.writeHead(200, {
    'Content-Type': 'text/event-stream; charset=utf-8',
    'Cache-Control': 'no-cache',
    Connection: 'keep-alive',
    'X-Accel-Buffering': 'no',
  });
}
function writeSseEvent(res, obj) {
  res.write('data: ' + JSON.stringify(obj) + '\n\n');
}

function errorToStatus(e) {
  if (e instanceof Qwen.QwenRateLimitedError) return 429;
  if (e instanceof Qwen.QwenUnauthorizedError) return 401;
  if (e instanceof Qwen.QwenBadRequestError) return 400;
  if (e instanceof Qwen.QwenNetworkError) return 502;
  return 500;
}
function errorToJson(e) {
  return {
    error: e.message,
    code: e.code || null,
    retryAfterHours: e.retryAfterHours || null,
  };
}

async function handle(req, res) {
  const url = new URL(req.url, 'http://localhost');
  const method = req.method || 'GET';

  // Permissive CORS — run behind a gateway that enforces auth.
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Methods', 'GET,POST,DELETE,OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type');
  if (method === 'OPTIONS') { res.writeHead(204); res.end(); return; }

  // ── GET /healthz ──
  if (method === 'GET' && url.pathname === '/healthz') {
    try {
      const sess = await Qwen.warmup();
      return writeJson(res, 200, {
        ok: true,
        session: {
          createdAt: sess.createdAt,
          expiresInSeconds: Math.round(sess.expiresIn / 1000),
        },
        chats: chats.size,
        models: Qwen.models,
      });
    } catch (e) {
      return writeJson(res, 500, { ok: false, error: e.message });
    }
  }

  // ── POST /v1/chat/completions (stateless, like OpenAI) ──
  if (method === 'POST' && url.pathname === '/v1/chat/completions') {
    let body;
    try { body = await readJson(req); }
    catch (e) { return writeJson(res, 400, { error: e.message }); }

    const {
      messages = [],
      model,
      system = null,
      stream = false,
      chat_mode = 'normal',
      chat_type = 't2t',
      thinking = false,
    } = body;

    if (!Array.isArray(messages) || !messages.length) {
      return writeJson(res, 400, { error: 'messages array required' });
    }

    // Collapse messages[] into a single system+user pair — Qwen's
    // one-message-per-turn API doesn't accept OpenAI history natively.
    const lastUser = [...messages].reverse().find((m) => m.role === 'user');
    if (!lastUser) return writeJson(res, 400, { error: 'no user message' });
    const transcript = messages
      .slice(0, -1)
      .map((m) => `${m.role}: ${m.content}`)
      .join('\n');
    const effectiveSystem = [system, transcript].filter(Boolean).join('\n\n') || null;

    try {
      const qwen = new Qwen({
        model,
        system: effectiveSystem,
        stream,
        chatMode: chat_mode,
        chatType: chat_type,
        thinking,
      });

      if (stream) {
        writeSseStart(res);
        for await (const ev of qwen.ask(lastUser.content)) {
          if (ev.type === 'text') {
            writeSseEvent(res, { delta: ev.content, fullContent: ev.fullContent });
          }
          if (ev.type === 'done') {
            writeSseEvent(res, { done: true, reply: ev.reply, usage: qwen.usage });
          }
        }
        res.end();
        return;
      }

      const { reply } = await qwen.ask(lastUser.content);
      return writeJson(res, 200, {
        model: qwen.options.model || Qwen.DEFAULT_MODEL,
        reply,
        usage: qwen.usage,
      });
    } catch (e) {
      if (REFRESH_ON_429 && e instanceof Qwen.QwenRateLimitedError) {
        try {
          await Qwen.warmup({ forceRefresh: true });
          const qwen = new Qwen({
            model, system: effectiveSystem, chatMode: chat_mode,
            chatType: chat_type, thinking,
          });
          const { reply } = await qwen.ask(lastUser.content);
          return writeJson(res, 200, { reply, usage: qwen.usage, rotated: true });
        } catch (e2) {
          return writeJson(res, errorToStatus(e2), errorToJson(e2));
        }
      }
      return writeJson(res, errorToStatus(e), errorToJson(e));
    }
  }

  // ── POST /v1/conversations ──
  if (method === 'POST' && url.pathname === '/v1/conversations') {
    let body;
    try { body = await readJson(req); }
    catch (e) { return writeJson(res, 400, { error: e.message }); }
    try {
      const qwen = new Qwen({
        model: body.model,
        system: body.system || null,
        chatMode: body.chat_mode || 'normal',
        chatType: body.chat_type || 't2t',
        thinking: !!body.thinking,
      });
      // Force eager chat creation so we have a chatId immediately
      const id = newId();
      chats.set(id, { qwen, createdAt: Date.now() });
      return writeJson(res, 201, { id, model: qwen.options.model || Qwen.DEFAULT_MODEL });
    } catch (e) {
      return writeJson(res, errorToStatus(e), errorToJson(e));
    }
  }

  // ── POST /v1/conversations/:id/messages ──
  const sendMatch = url.pathname.match(/^\/v1\/conversations\/([^/]+)\/messages$/);
  if (method === 'POST' && sendMatch) {
    const id = sendMatch[1];
    const entry = chats.get(id);
    if (!entry) return writeJson(res, 404, { error: 'conversation not found' });
    let body;
    try { body = await readJson(req); }
    catch (e) { return writeJson(res, 400, { error: e.message }); }
    const { content, stream = false, method: turnMethod = 'ask' } = body;
    if (!content) return writeJson(res, 400, { error: 'content required' });
    if (!['ask', 'search', 'image', 'think'].includes(turnMethod)) {
      return writeJson(res, 400, { error: 'method must be ask|search|image|think' });
    }

    try {
      // Since the server-side chat was created without `stream`, we
      // always use the promise form here. Streaming responses are served
      // by wrapping the promise's internal iterator manually.
      const qwen = entry.qwen;
      if (stream) {
        // Create a stream-capable wrapper over the same chat_id
        const streamer = new Qwen({
          ...qwen.options,
          stream: true,
          chatId: qwen.chatId,
          lastResponseId: qwen.lastResponseId,
        });
        writeSseStart(res);
        for await (const ev of streamer[turnMethod](content)) {
          if (ev.type === 'text') writeSseEvent(res, { delta: ev.content, fullContent: ev.fullContent });
          if (ev.type === 'thinking') writeSseEvent(res, { thinking: ev.content });
          if (ev.type === 'sources') writeSseEvent(res, { sources: ev.sources });
          if (ev.type === 'image') writeSseEvent(res, { image: { url: ev.url, width: ev.width, height: ev.height } });
          if (ev.type === 'done') writeSseEvent(res, { done: true, reply: ev.reply, usage: streamer.usage });
        }
        // Write state back to the stored chat
        qwen.chatId = streamer.chatId;
        qwen.lastResponseId = streamer.lastResponseId;
        qwen.usage = streamer.usage;
        qwen.history = streamer.history;
        res.end();
        return;
      }

      const result = await qwen[turnMethod](content);
      return writeJson(res, 200, { ...result, usage: qwen.usage });
    } catch (e) {
      return writeJson(res, errorToStatus(e), errorToJson(e));
    }
  }

  // ── DELETE /v1/conversations/:id ──
  const delMatch = url.pathname.match(/^\/v1\/conversations\/([^/]+)$/);
  if (method === 'DELETE' && delMatch) {
    chats.delete(delMatch[1]);
    return writeJson(res, 200, { ok: true });
  }

  writeJson(res, 404, { error: 'not found' });
}

// ──── boot ────
(async () => {
  console.log(`[qwen-server] booting pid=${process.pid}`);
  const t0 = Date.now();
  try {
    const w = await Qwen.warmup();
    console.log(
      `[qwen-server] warmup OK in ${Date.now() - t0}ms, ` +
      `session expires in ${Math.round(w.expiresIn / 1000)}s`
    );
  } catch (e) {
    console.error('[qwen-server] warmup failed:', e.message);
  }

  const server = http.createServer((req, res) => {
    handle(req, res).catch((e) => {
      console.error('[qwen-server] handler error:', e);
      try { writeJson(res, 500, { error: e.message }); } catch {}
    });
  });
  server.listen(PORT, () => {
    console.log(`[qwen-server] listening on http://0.0.0.0:${PORT}`);
    console.log('  GET    /healthz');
    console.log('  POST   /v1/chat/completions   { messages, stream?, model?, system?, chat_type? }');
    console.log('  POST   /v1/conversations      { model?, system?, chat_type?, ... }');
    console.log('  POST   /v1/conversations/:id/messages  { content, stream?, method: ask|search|image|think }');
    console.log('  DELETE /v1/conversations/:id');
  });

  const shutdown = (sig) => {
    console.log(`[qwen-server] ${sig}, shutting down`);
    server.close(() => process.exit(0));
  };
  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT', () => shutdown('SIGINT'));
})();
