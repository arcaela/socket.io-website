// src/lib/conversation.ts
// Stateful multi-turn conversation handle with send() + stream() forms
// emitting typed events.
import { API, DEFAULT_MODEL } from './constants';
import { baseHeaders, createChat, streamChatCompletion } from './http';
import { QwenError } from './errors';
import type {
  QwenModel,
  ChatMode,
  ChatType,
  SavedSession,
  Source,
  StreamEvent,
  DoneEvent,
  Message,
  TurnUsage,
} from './types';

function extractSourcesFromToolEvent(ev: any): Source[] {
  const out: Source[] = [];
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

export interface ConversationOptions {
  model?: QwenModel;
  chatMode?: ChatMode;
  chatType?: ChatType;
  system?: string | null;
  thinking?: boolean;
  /** Skip creating a new chat — reuse this existing chat_id. */
  chatId?: string;
  /** Seed lastResponseId for parent_id chaining. */
  lastResponseId?: string | null;
}

export interface SendOptions {
  chatType?: ChatType;
  chatMode?: ChatMode;
  thinking?: boolean;
}

export interface Conversation {
  readonly chatId: string;
  readonly model: QwenModel;
  readonly chatMode: ChatMode;
  readonly chatType: ChatType;
  readonly turn: number;
  readonly history: Message[];
  readonly lastResponseId: string | null;
  send(message: string, sendOpts?: SendOptions): Promise<DoneEvent>;
  stream(message: string, sendOpts?: SendOptions): AsyncGenerator<StreamEvent, void, void>;
}

export interface GetSessionFn {
  (opts?: { forceRefresh?: boolean }): Promise<SavedSession>;
}

export function makeConversation({ getSession }: { getSession: GetSessionFn }) {
  return async function conversation(opts: ConversationOptions = {}): Promise<Conversation> {
    const {
      model = DEFAULT_MODEL,
      chatMode = 'normal',
      chatType = 't2t',
      system = null,
      thinking = false,
      chatId: existingChatId,
      lastResponseId: seedLastResponseId = null,
    } = opts;

    const session = await getSession();

    // If the caller provided an existing chatId (session recovery), reuse
    // it. Otherwise create a fresh one on the backend.
    const chatId: string = existingChatId || (await createChat(session, { model, chatMode, chatType }));

    let lastResponseId: string | null = seedLastResponseId;
    let turnCount = 0;
    const history: Message[] = [];
    // Track whether the system prompt has been injected yet (it goes on
    // the first turn only to save tokens on subsequent turns).
    let systemInjected = existingChatId != null; // assume already done if resuming

    async function* _sendStream(message: string, sendOpts: SendOptions = {}): AsyncGenerator<StreamEvent, void, void> {
      const useChatType = sendOpts.chatType || chatType;
      const useChatMode = sendOpts.chatMode || chatMode;
      const useThinking = sendOpts.thinking != null ? sendOpts.thinking : thinking;

      let prompt = message;
      if (system && !systemInjected) {
        prompt = `[Instrucciones del sistema]\n${system}\n\n[Mensaje del usuario]\n${message}`;
        systemInjected = true;
      }

      let answerText = '';
      let thinkingText = '';
      let usage: TurnUsage | null = null;
      let responseId: string | null = null;
      const sources: Source[] = [];
      const toolEvents: any[] = [];
      let imageMeta: { url: string; width: number | null; height: number | null } | null = null;

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
          yield { type: 'info', info: ev.info };
          continue;
        }

        if (ev.type === 'tool') {
          toolEvents.push(ev);

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
          if (ev.responseId) responseId = ev.responseId;
          continue;
        }
      }

      // ---- state mutation + terminal done event ----
      if (responseId) lastResponseId = responseId;
      turnCount += 1;
      history.push({ role: 'user', content: message });
      history.push({
        role: 'assistant',
        content: answerText,
        thinking: thinkingText || undefined,
      });

      const done: DoneEvent = {
        type: 'done',
        reply: answerText,
        thinking: thinkingText || null,
        sources: sources.length ? sources : null,
        image: imageMeta,
        usage,
        responseId,
        parentId: lastResponseId,
        turn: turnCount,
        toolEvents,
      };
      yield done;
    }

    function stream(message: string, sendOpts: SendOptions = {}) {
      return _sendStream(message, sendOpts);
    }

    async function send(message: string, sendOpts: SendOptions = {}): Promise<DoneEvent> {
      let done: DoneEvent | null = null;
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
