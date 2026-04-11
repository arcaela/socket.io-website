#!/usr/bin/env node
// server.js — tiny HTTP front for `require('./src')`. Designed to be the
// main process of a Docker container, so one qwen() client is warmed up
// and reused across every request.
//
//   PORT                   listening port (default 8787)
//   QWEN_CACHE_DIR         override session cache dir
//   QWEN_REFRESH_ON_429    set to "0" to disable auto-rotate on RateLimited
//
// Endpoints:
//   GET    /healthz                           — liveness + session metadata
//   POST   /v1/chat/completions               — OpenAI-ish, single turn
//   POST   /v1/conversations                  — create a server-side conversation
//   POST   /v1/conversations/:id/messages     — send one turn (stateful)
//   DELETE /v1/conversations/:id              — drop from memory
'use strict';

const http = require('node:http');
const crypto = require('node:crypto');
const qwen = require('./src');

const PORT = parseInt(process.env.PORT || '8787', 10);
const REFRESH_ON_429 = process.env.QWEN_REFRESH_ON_429 !== '0';

// In-process conversation store. Keep it bounded — a real deployment would
// push this to Redis / a DB keyed by a per-user session id.
const conversations = new Map();
function newConvId() { return crypto.randomUUID(); }

// ---- request helpers ----
function readJson(req) {
  return new Promise((resolve, reject) => {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
      try { resolve(body ? JSON.parse(body) : {}); }
      catch (_) { reject(new Error('invalid JSON body')); }
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

// ---- route dispatcher ----
async function handle(req, res) {
  const url = new URL(req.url, 'http://localhost');
  const method = req.method || 'GET';

  // CORS — this server is intended to run behind a gateway; the gateway
  // should enforce auth and tighten CORS. We leave everything open here.
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Methods', 'GET,POST,DELETE,OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type');
  if (method === 'OPTIONS') { res.writeHead(204); res.end(); return; }

  // GET /healthz
  if (method === 'GET' && url.pathname === '/healthz') {
    try {
      const s = await qwen.getSession();
      return writeJson(res, 200, {
        ok: true,
        session: {
          createdAt: s.createdAt,
          ageSeconds: Math.round((Date.now() - s.createdAt) / 1000),
        },
        conversations: conversations.size,
      });
    } catch (e) {
      return writeJson(res, 500, { ok: false, error: e.message });
    }
  }

  // POST /v1/chat/completions  — stateless, OpenAI-ish
  if (method === 'POST' && url.pathname === '/v1/chat/completions') {
    let body;
    try { body = await readJson(req); }
    catch (e) { return writeJson(res, 400, { error: e.message }); }

    const {
      messages = [],
      model = qwen.DEFAULT_MODEL,
      system = null,
      stream = false,
      chat_mode = 'normal',
      chat_type = 't2t',
      thinking = false,
    } = body;

    if (!Array.isArray(messages) || !messages.length) {
      return writeJson(res, 400, { error: 'messages array required' });
    }

    try {
      if (stream) {
        writeSseStart(res);
        let reply = '';
        for await (const ev of qwen.stream(messages, {
          model, system, chatType: chat_type, chatMode: chat_mode, thinkingEnabled: thinking,
        })) {
          if (ev.type === 'delta') {
            reply = ev.fullContent;
            writeSseEvent(res, { delta: ev.content, fullContent: ev.fullContent });
          }
        }
        writeSseEvent(res, { done: true, reply });
        res.end();
        return;
      }
      const reply = await qwen(messages, {
        model, system, chatType: chat_type, chatMode: chat_mode, thinkingEnabled: thinking,
      });
      return writeJson(res, 200, { model, reply });
    } catch (e) {
      if (REFRESH_ON_429 && e.code === 'RateLimited') {
        try {
          await qwen.warmup({ forceRefresh: true });
          const reply = await qwen(messages, {
            model, system, chatType: chat_type, chatMode: chat_mode, thinkingEnabled: thinking,
          });
          return writeJson(res, 200, { model, reply, rotated: true });
        } catch (e2) {
          return writeJson(res, 429, {
            error: e2.message, code: e2.code || 'RateLimited', retryAfterHours: e2.retryAfterHours,
          });
        }
      }
      return writeJson(res, 500, {
        error: e.message, code: e.code, retryAfterHours: e.retryAfterHours,
      });
    }
  }

  // POST /v1/conversations — create a new server-side conversation handle
  if (method === 'POST' && url.pathname === '/v1/conversations') {
    let body;
    try { body = await readJson(req); }
    catch (e) { return writeJson(res, 400, { error: e.message }); }
    try {
      const conv = await qwen.conversation({
        model: body.model,
        system: body.system || null,
        chatMode: body.chat_mode || 'normal',
        chatType: body.chat_type || 't2t',
        thinking: !!body.thinking,
      });
      const id = newConvId();
      conversations.set(id, { conv, createdAt: Date.now() });
      return writeJson(res, 201, { id, chatId: conv.chatId, model: conv.model });
    } catch (e) {
      return writeJson(res, 500, { error: e.message });
    }
  }

  // POST /v1/conversations/:id/messages — send one turn
  const sendMatch = url.pathname.match(/^\/v1\/conversations\/([^/]+)\/messages$/);
  if (method === 'POST' && sendMatch) {
    const id = sendMatch[1];
    const entry = conversations.get(id);
    if (!entry) return writeJson(res, 404, { error: 'conversation not found' });
    let body;
    try { body = await readJson(req); }
    catch (e) { return writeJson(res, 400, { error: e.message }); }
    const { content, stream = false } = body;
    if (!content) return writeJson(res, 400, { error: 'content required' });
    try {
      if (stream) {
        writeSseStart(res);
        const r = await entry.conv.send(content, {
          onDelta: (d) => writeSseEvent(res, { delta: d.content, fullContent: d.fullContent }),
        });
        writeSseEvent(res, { done: true, reply: r.reply, turn: r.turn, usage: r.usage });
        res.end();
        return;
      }
      const r = await entry.conv.send(content);
      return writeJson(res, 200, {
        reply: r.reply, turn: r.turn, usage: r.usage, thinking: r.thinking, toolEvents: r.toolEvents,
      });
    } catch (e) {
      return writeJson(res, e.code === 'RateLimited' ? 429 : 500, {
        error: e.message, code: e.code, retryAfterHours: e.retryAfterHours,
      });
    }
  }

  // DELETE /v1/conversations/:id
  const delMatch = url.pathname.match(/^\/v1\/conversations\/([^/]+)$/);
  if (method === 'DELETE' && delMatch) {
    conversations.delete(delMatch[1]);
    return writeJson(res, 200, { ok: true });
  }

  writeJson(res, 404, { error: 'not found' });
}

// ---- boot ----
(async () => {
  console.log(`[qwen-server] booting pid=${process.pid}`);
  const t0 = Date.now();
  try {
    const w = await qwen.warmup();
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
      try { writeJson(res, 500, { error: e.message }); } catch (_) {}
    });
  });
  server.listen(PORT, () => {
    console.log(`[qwen-server] listening on http://0.0.0.0:${PORT}`);
    console.log('[qwen-server] endpoints:');
    console.log('  GET    /healthz');
    console.log('  POST   /v1/chat/completions   { messages, stream?, model?, system?, chat_type? }');
    console.log('  POST   /v1/conversations      { model?, system?, chat_type?, ... }');
    console.log('  POST   /v1/conversations/:id/messages  { content, stream? }');
    console.log('  DELETE /v1/conversations/:id');
  });

  const shutdown = (sig) => {
    console.log(`[qwen-server] ${sig}, shutting down`);
    server.close(() => process.exit(0));
  };
  process.on('SIGTERM', () => shutdown('SIGTERM'));
  process.on('SIGINT', () => shutdown('SIGINT'));
})();
