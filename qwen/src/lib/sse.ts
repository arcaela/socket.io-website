// src/lib/sse.ts
// Tiny SSE parser for Qwen's `text/event-stream` responses. Every event is
// a single `data: {json}\n\n` block, so we don't need to handle multi-line
// data: fields or the full SSE spec.

export function parseSSEBlock(block: string): any | null {
  const lines = block.split('\n');
  const dataLines: string[] = [];
  for (const line of lines) {
    if (line.startsWith('data:')) dataLines.push(line.slice(5).trimStart());
  }
  if (!dataLines.length) return null;
  try {
    return JSON.parse(dataLines.join('\n'));
  } catch {
    return null;
  }
}
