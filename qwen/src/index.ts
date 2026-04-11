// src/index.ts
// Public entry point. Exports `Qwen` as the default export — a class whose
// constructor options define the chat's behavior (stream/promise, model,
// system prompt, session recovery) and whose static methods are shortcuts
// that create a throwaway instance.
//
// Usage:
//
//   import Qwen from 'qwen-node';
//
//   // Instance: stateful chat
//   const chat = new Qwen({ model: 'qwen3.6-plus', system: 'Be brief.' });
//   const { reply } = await chat.ask('hola');
//
//   // Stream mode: every method returns an async iterator
//   const streaming = new Qwen({ stream: true });
//   for await (const ev of streaming.ask('count to 5')) {
//     if (ev.type === 'text') process.stdout.write(ev.content);
//   }
//
//   // Static shortcuts
//   const reply = await Qwen.ask('hola');
//   const { reply, sources } = await Qwen.search('latest news');
//   const { url } = await Qwen.image('orange cat');
//   const { reply, thinking } = await Qwen.think('explain relativity');
//
//   // Session recovery
//   const saved = chat.toJSON();
//   // ... persist saved somewhere ...
//   const resumed = new Qwen({ ...saved.options, ...saved });
//
// ⚠️ Require-time side effects: this module eagerly wires undici's proxy
// and (unless a valid cached session exists) immediately builds the jsdom
// env and evaluates Alibaba's AWSC scripts. See README § ordering quirk.

import './lib/proxy-setup';

import {
  DEFAULT_MODEL,
  BASE,
  API,
  KNOWN_MODELS,
  SESSION_TTL_MS,
} from './lib/constants';
import { getAwscScripts } from './lib/awsc-scripts';
import { createJsdomEnv, JsdomEnv } from './lib/jsdom-env';
import {
  loadCachedSession,
  saveSession,
  injectSession,
  makeGetSession,
  GetSessionFn,
} from './lib/session';
import { createChat } from './lib/http';
import { makeConversation, Conversation } from './lib/conversation';
import {
  QwenError,
  QwenRateLimitedError,
  QwenUnauthorizedError,
  QwenBadRequestError,
  QwenServerError,
  QwenNetworkError,
} from './lib/errors';

import type {
  QwenOptions,
  QwenUsage,
  QwenState,
  QwenModel,
  ChatType,
  ChatMode,
  SavedSession,
  Message,
  Source,
  TurnUsage,
  AskResult,
  SearchResult,
  ImageResult,
  ThinkResult,
  StreamEvent,
  DoneEvent,
} from './lib/types';

// ---------- module-level jsdom bootstrap ----------

let _jsdomEnv: JsdomEnv | null = null;

function ensureJsdomEnv(): JsdomEnv {
  if (_jsdomEnv) return _jsdomEnv;
  const scripts = getAwscScripts();
  if (!scripts) {
    throw new Error(
      'qwen: could not obtain AWSC scripts (network unreachable and no bundle embedded?)'
    );
  }
  _jsdomEnv = createJsdomEnv(scripts);
  return _jsdomEnv;
}

// Skip the jsdom setup at require-time if we have a valid session cached.
// This is the hot path for warm starts (~400ms instead of ~10s).
(function initAtRequireTime() {
  try {
    if (loadCachedSession()) return;
  } catch {}
  try {
    ensureJsdomEnv();
  } catch (e: any) {
    if (process.env.QWEN_DEBUG) {
      console.error('[qwen] require-time init failed:', e.message);
    }
  }
})();

const getSession: GetSessionFn = makeGetSession({ ensureJsdomEnv });
const conversation = makeConversation({ getSession });

// ---------- helpers shared by instance + static methods ----------

function extractSources(done: DoneEvent): Source[] {
  return done.sources || [];
}

function extractImage(done: DoneEvent, model: QwenModel): ImageResult {
  const img = done.image || ({} as any);
  return {
    url: img.url || done.reply.trim(),
    width: img.width ?? null,
    height: img.height ?? null,
    model,
    turn: done.turn,
  };
}

// ---------- the Qwen class ----------

export default class Qwen {
  // =========================================================================
  // Static surface
  // =========================================================================

  /** Known models accepted by the guest endpoint. */
  static readonly models: readonly QwenModel[] = KNOWN_MODELS;

  /** Base URL of the backend (for debugging / custom probes). */
  static readonly BASE: string = BASE;

  /** Default model id used when none is specified. */
  static readonly DEFAULT_MODEL: QwenModel = DEFAULT_MODEL;

  // Error classes re-exposed as static members for convenience
  static readonly QwenError = QwenError;
  static readonly QwenRateLimitedError = QwenRateLimitedError;
  static readonly QwenUnauthorizedError = QwenUnauthorizedError;
  static readonly QwenBadRequestError = QwenBadRequestError;
  static readonly QwenServerError = QwenServerError;
  static readonly QwenNetworkError = QwenNetworkError;

  /**
   * Pre-initialize the session cache. Call this on container startup.
   * Returns metadata about the current session.
   */
  static async warmup(opts: { forceRefresh?: boolean } = {}): Promise<{
    ok: boolean;
    createdAt: number;
    expiresIn: number;
  }> {
    if (opts.forceRefresh) ensureJsdomEnv();
    const s = await getSession({ forceRefresh: opts.forceRefresh });
    return {
      ok: true,
      createdAt: s.createdAt,
      expiresIn: SESSION_TTL_MS - (Date.now() - s.createdAt),
    };
  }

  /** Fetch the live list of available models from `/api/models`. */
  static async fetchModels(): Promise<any[]> {
    const session = await getSession();
    const res = await fetch(`${BASE}/api/models`, {
      headers: {
        'User-Agent': session.userAgent,
        Version: '0.2.37',
        source: 'web',
        'bx-ua': session.bxUa,
        'bx-umidtoken': session.bxUmidtoken,
        'bx-v': session.bxV,
        Cookie: session.cookieHeader,
        Origin: BASE,
        Referer: BASE + '/',
      },
    });
    const json = (await res.json()) as any;
    return (json && json.data) || [];
  }

  /**
   * One-shot plain text ask. The second argument is a full `QwenOptions`
   * object used to construct a throwaway instance.
   */
  static async ask(prompt: string, opts: QwenOptions = {}): Promise<string> {
    const chat = new Qwen({ ...opts, stream: false });
    const { reply } = await (chat.ask(prompt) as Promise<AskResult>);
    return reply;
  }

  static async search(query: string, opts: QwenOptions = {}): Promise<SearchResult> {
    const chat = new Qwen({ ...opts, stream: false });
    return (await chat.search(query)) as SearchResult;
  }

  static async image(prompt: string, opts: QwenOptions = {}): Promise<ImageResult> {
    const chat = new Qwen({ ...opts, stream: false });
    return (await chat.image(prompt)) as ImageResult;
  }

  static async think(prompt: string, opts: QwenOptions = {}): Promise<ThinkResult> {
    const chat = new Qwen({ ...opts, stream: false });
    return (await chat.think(prompt)) as ThinkResult;
  }

  // =========================================================================
  // Instance surface
  // =========================================================================

  readonly options: Readonly<QwenOptions>;
  readonly stream: boolean;

  /** Server-side chat_id. Undefined until the first turn completes. */
  chatId: string | null;

  /** Last response_id, used as parent_id for the next turn. */
  lastResponseId: string | null;

  /** Accumulated token usage across all turns of this chat. */
  usage: QwenUsage;

  /** Local transcript of the conversation. */
  history: Message[];

  // Lazy conversation instance — created on first call to an instance method
  private _conv: Conversation | null = null;
  private _convPromise: Promise<Conversation> | null = null;

  constructor(opts: QwenOptions = {}) {
    this.options = Object.freeze({ ...opts });
    this.stream = !!opts.stream;

    this.chatId = opts.chatId ?? null;
    this.lastResponseId = opts.lastResponseId ?? null;
    this.history = opts.history ? opts.history.slice() : [];
    this.usage = opts.usage
      ? { ...opts.usage }
      : { input: 0, output: 0, tokens: 0, requests: 0 };

    // If the caller supplied an existing session (from a previous
    // chat.exportSession()), inject it into the on-disk cache so the
    // next getSession() picks it up. The cache is also consulted for
    // automatic token rotation.
    if (opts.session) {
      try {
        injectSession(opts.session);
      } catch {}
    }
  }

  // ---- lazy conversation creation ----
  private async _ensureConv(): Promise<Conversation> {
    if (this._conv) return this._conv;
    if (this._convPromise) return this._convPromise;

    this._convPromise = conversation({
      model: this.options.model ?? DEFAULT_MODEL,
      chatMode: this.options.chatMode ?? 'normal',
      chatType: this.options.chatType ?? 't2t',
      system: this.options.system ?? null,
      thinking: this.options.thinking ?? false,
      chatId: this.chatId || undefined,
      lastResponseId: this.lastResponseId,
    }).then((c) => {
      this._conv = c;
      this.chatId = c.chatId;
      return c;
    });

    return this._convPromise;
  }

  // ---- usage accumulator ----
  private _accumulateUsage(usage: TurnUsage | null): void {
    if (!usage) return;
    const input = usage.input_tokens || 0;
    const output = usage.output_tokens || 0;
    this.usage.input += input;
    this.usage.output += output;
    this.usage.tokens += input + output;
    this.usage.requests += 1;
  }

  // ---- one send — Promise form ----
  private async _doPromise(
    message: string,
    sendOpts: { chatType?: ChatType; thinking?: boolean } = {}
  ): Promise<DoneEvent> {
    const conv = await this._ensureConv();
    const done = await conv.send(message, sendOpts);
    this.chatId = conv.chatId;
    this.lastResponseId = conv.lastResponseId;
    this._accumulateUsage(done.usage);
    // Append to our local history
    this.history.push({ role: 'user', content: message });
    this.history.push({
      role: 'assistant',
      content: done.reply,
      thinking: done.thinking || undefined,
    });
    return done;
  }

  // ---- one send — Stream form ----
  private async *_doStream(
    message: string,
    sendOpts: { chatType?: ChatType; thinking?: boolean } = {}
  ): AsyncGenerator<StreamEvent, void, void> {
    const conv = await this._ensureConv();
    for await (const ev of conv.stream(message, sendOpts)) {
      // Update state BEFORE yielding `done` so consumers who read
      // `chat.usage` / `chat.chatId` right after receiving `done` see
      // the fresh values (otherwise there's a one-tick lag).
      if (ev.type === 'done') {
        this.chatId = conv.chatId;
        this.lastResponseId = conv.lastResponseId;
        this._accumulateUsage(ev.usage);
        this.history.push({ role: 'user', content: message });
        this.history.push({
          role: 'assistant',
          content: ev.reply,
          thinking: ev.thinking || undefined,
        });
      }
      yield ev;
    }
  }

  // =========================================================================
  // Instance methods
  //
  // Every method returns a Promise when `stream: false` (default) and an
  // async iterator when `stream: true`. TypeScript users who want precise
  // return types can narrow based on `this.stream`.
  // =========================================================================

  ask(message: string): Promise<AskResult> | AsyncGenerator<StreamEvent, void, void> {
    if (this.stream) {
      return this._doStream(message);
    }
    return (async () => {
      const done = await this._doPromise(message);
      return {
        reply: done.reply,
        turn: done.turn,
        usage: done.usage,
        thinking: done.thinking,
      };
    })();
  }

  search(query: string): Promise<SearchResult> | AsyncGenerator<StreamEvent, void, void> {
    if (this.stream) {
      return this._doStream(query, { chatType: 'search' });
    }
    return (async () => {
      const done = await this._doPromise(query, { chatType: 'search' });
      return {
        reply: done.reply,
        sources: extractSources(done),
        turn: done.turn,
        usage: done.usage,
      };
    })();
  }

  image(prompt: string): Promise<ImageResult> | AsyncGenerator<StreamEvent, void, void> {
    if (this.stream) {
      return this._doStream(prompt, { chatType: 't2i' });
    }
    return (async () => {
      const done = await this._doPromise(prompt, { chatType: 't2i' });
      return extractImage(done, this.options.model ?? DEFAULT_MODEL);
    })();
  }

  think(message: string): Promise<ThinkResult> | AsyncGenerator<StreamEvent, void, void> {
    if (this.stream) {
      return this._doStream(message, { thinking: true });
    }
    return (async () => {
      const done = await this._doPromise(message, { thinking: true });
      return {
        reply: done.reply,
        thinking: done.thinking,
        turn: done.turn,
        usage: done.usage,
      };
    })();
  }

  // =========================================================================
  // Session export / import
  // =========================================================================

  /**
   * Snapshot the full chat state for persistence. Pass this back to a new
   * `Qwen(...)` constructor to resume the conversation later.
   */
  toJSON(): QwenState {
    return {
      chatId: this.chatId,
      lastResponseId: this.lastResponseId,
      usage: { ...this.usage },
      history: this.history.slice(),
      options: { ...this.options },
      session: null, // callers who need the session credentials call exportSession() explicitly
    };
  }

  /**
   * Export the live session credentials alongside the chat state. Useful
   * if you want to persist a resumable chat across process restarts within
   * the session TTL (25 minutes).
   */
  async exportSession(): Promise<QwenState> {
    const state = this.toJSON();
    try {
      state.session = await getSession();
    } catch {
      state.session = null;
    }
    return state;
  }
}

// Ensure CommonJS consumers get the class as the direct export. Without
// this, `const Qwen = require('qwen-node')` would be `{ default: Qwen }`.
// With this, BOTH `require('qwen-node')` and `import Qwen from 'qwen-node'`
// yield the class directly.
module.exports = Qwen;
module.exports.default = Qwen;
module.exports.Qwen = Qwen;

// Named re-exports for consumers who prefer them
module.exports.QwenError = QwenError;
module.exports.QwenRateLimitedError = QwenRateLimitedError;
module.exports.QwenUnauthorizedError = QwenUnauthorizedError;
module.exports.QwenBadRequestError = QwenBadRequestError;
module.exports.QwenServerError = QwenServerError;
module.exports.QwenNetworkError = QwenNetworkError;

// Re-export types for TypeScript consumers (erased at runtime)
export type {
  QwenOptions,
  QwenUsage,
  QwenState,
  QwenModel,
  ChatType,
  ChatMode,
  SavedSession,
  Message,
  Source,
  TurnUsage,
  AskResult,
  SearchResult,
  ImageResult,
  ThinkResult,
  StreamEvent,
  DoneEvent,
};

export {
  QwenError,
  QwenRateLimitedError,
  QwenUnauthorizedError,
  QwenBadRequestError,
  QwenServerError,
  QwenNetworkError,
};
