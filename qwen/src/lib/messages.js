// src/lib/messages.js
// Translate an OpenAI-style messages array into a single Qwen-friendly
// prompt string. Qwen's /api/v2/chat/completions accepts ONLY ONE element
// in its `messages` array (verified empirically — anything else returns
// `Bad_Request: Invalid input too many messages.`). When callers pass a
// multi-turn history à la OpenAI we flatten it into a transcript that the
// backend treats as a single user turn.
//
// For true multi-turn with server-side memory, use qwen.conversation(),
// which reuses chat_id + parent_id to let the backend remember context.
'use strict';

function flattenMessages(messages) {
  const systemParts = [];
  const turnParts = [];
  for (const m of messages) {
    if (!m || typeof m.content !== 'string') continue;
    if (m.role === 'system') {
      systemParts.push(m.content.trim());
    } else if (m.role === 'user') {
      turnParts.push(`User: ${m.content.trim()}`);
    } else if (m.role === 'assistant') {
      turnParts.push(`Assistant: ${m.content.trim()}`);
    }
  }
  if (!messages.length || messages[messages.length - 1].role !== 'user') {
    turnParts.push('User:');
  }
  const sys = systemParts.length ? systemParts.join('\n\n') + '\n\n' : '';
  return sys + turnParts.join('\n\n');
}

module.exports = { flattenMessages };
