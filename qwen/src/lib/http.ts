// src/lib/http.ts
// Low-level Qwen API calls. Throws typed QwenError subclasses on failure.
import * as crypto from 'node:crypto';
import { BASE, API, SPA_VERSION, DEFAULT_MODEL } from './constants';
import { parseSSEBlock } from './sse';
import {
  QwenError,
  errorFromEnvelope,
  wrapNetworkError,
} from './errors';
import type { SavedSession, ChatType, ChatMode, QwenModel } from './types';

export function baseHeaders(session: SavedSession, extra: Record<string, string> = {}): Record<string, string> {
  return {
    Accept: 'application/json, text/plain, */*',
    'Accept-Language': 'en-US',
    'Content-Type': 'application/json',
    Origin: BASE,
    Referer: BASE + '/',
    'User-Agent': session.userAgent,
    Version: SPA_VERSION,
    source: 'web',
    'X-Request-Id': crypto.randomUUID(),
    'bx-ua': session.bxUa,
    'bx-umidtoken': session.bxUmidtoken,
    'bx-v': session.bxV,
    Cookie: session.cookieHeader,
    ...extra,
  };
}

export interface CreateChatOptions {
  model?: QwenModel;
  chatMode?: ChatMode;
  chatType?: ChatType;
}

export async function createChat(
  session: SavedSession,
  opts: CreateChatOptions = {}
): Promise<string> {
  const {
    model = DEFAULT_MODEL,
    chatMode = 'normal',
    chatType = 't2t',
  } = opts;

  let res: Response;
  try {
    res = await fetch(`${API}/chats/new`, {
      method: 'POST',
      headers: baseHeaders(session),
      body: JSON.stringify({
        title: 'New Chat',
        models: [model],
        chat_mode: chatMode,
        chat_type: chatType,
        timestamp: Date.now(),
      }),
    });
  } catch (e: any) {
    throw wrapNetworkError(e, 'createChat: fetch failed');
  }

  const text = await res.text();
  let json: any = null;
  try { json = JSON.parse(text); } catch {}
  if (json && json.success === false) {
    throw errorFromEnvelope(json, text);
  }
  if (!res.ok || !json || json.success !== true) {
    throw new QwenError(
      `createChat failed HTTP ${res.status}: ${text.slice(0, 300)}`,
      { status: res.status, response: json || text }
    );
  }
  return json.data.id;
}

// Raw events yielded by streamChatCompletion. These are then translated
// into the typed "StreamEvent" shape by conversation.ts.
export type RawEvent =
  | { type: 'created'; chatId: string; responseId: string | null; parentId: string | null }
  | { type: 'info'; info: any }
  | { type: 'delta'; content: string; fullContent: string; phase: string | undefined; role: string | undefined; usage: any; functionCall: any; functionId: any; extra: any }
  | { type: 'tool'; phase: string; role: string; functionCall: any; functionId: any; name: string; extra: any }
  | { type: 'finished'; fullContent: string; responseId: string | null };

export interface StreamOptions {
  model?: QwenModel;
  chatMode?: ChatMode;
  chatType?: ChatType;
  parentId?: string | null;
  thinkingEnabled?: boolean;
}

export async function* streamChatCompletion(
  session: SavedSession,
  chatId: string,
  prompt: string,
  opts: StreamOptions = {}
): AsyncGenerator<RawEvent, void, void> {
  const {
    model = DEFAULT_MODEL,
    chatMode = 'normal',
    chatType = 't2t',
    parentId = null,
    thinkingEnabled = false,
  } = opts;

  const body = {
    stream: true,
    version: '2.1',
    incremental_output: true,
    chat_id: chatId,
    chat_mode: chatMode,
    model,
    parent_id: parentId,
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

  let res: Response;
  try {
    res = await fetch(
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
  } catch (e: any) {
    throw wrapNetworkError(e, 'streamChatCompletion: fetch failed');
  }

  if (!res.ok) {
    const t = await res.text().catch(() => '');
    throw new QwenError(`chat/completions HTTP ${res.status}: ${t.slice(0, 300)}`, {
      status: res.status,
      response: t,
    });
  }

  const ct = res.headers.get('content-type') || '';
  if (!ct.includes('text/event-stream')) {
    const text = await res.text().catch(() => '');
    let parsed: any = null;
    try { parsed = JSON.parse(text); } catch {}
    throw errorFromEnvelope(parsed, text);
  }

  const reader = (res.body as any).getReader();
  const decoder = new TextDecoder('utf-8');
  let buffer = '';
  let fullContent = '';
  let responseId: string | null = null;

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

      // Record tool events FIRST — tool_result arrives with status:finished
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
      }

      if (delta.status === 'finished') {
        yield { type: 'finished', fullContent, responseId: payload.response_id || responseId };
        continue;
      }

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
