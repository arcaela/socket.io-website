// src/lib/sse.js
// Tiny SSE parser for Qwen's `text/event-stream` responses.
// Qwen's stream format is extremely plain: each event is a single `data:`
// line followed by a blank line. The payload is always JSON. We don't need
// to handle multi-line data: fields or the `event:` / `id:` / `retry:`
// metadata that the real SSE spec allows.
'use strict';

function parseSSEBlock(block) {
  const lines = block.split('\n');
  const dataLines = [];
  for (const line of lines) {
    if (line.startsWith('data:')) dataLines.push(line.slice(5).trimStart());
  }
  if (!dataLines.length) return null;
  try { return JSON.parse(dataLines.join('\n')); }
  catch (_) { return null; }
}

module.exports = { parseSSEBlock };
