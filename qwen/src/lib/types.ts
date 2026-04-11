// src/lib/types.ts
// Shared type definitions used across the whole module.

// -------- domain enums --------

export type QwenModel =
  | 'qwen3.6-plus'
  | 'qwen3.5-plus'
  | 'qwen3.5-omni-plus';

/** All the `chat_type` values Qwen's frontend enum publishes. */
export type ChatType =
  | 't2t' | 'search' | 'thinking' | 't2i' | 't2v' | 'i2v'
  | 'image_edit' | 'web_dev' | 'artifacts' | 'deep_research'
  | 'deep_research_webdev' | 'aipodcast' | 'travel' | 'travel_research'
  | 'travel_feedback' | 'learn' | 'slides' | 'translate' | 'mcp'
  | 'interrupt';

/** `chat_mode` values from the frontend. For guests the backend still accepts "normal". */
export type ChatMode = 'normal' | 'guest' | 'community' | 'local';

// -------- session --------

/** Minimum state needed to talk to the Qwen backend as a guest. */
export interface SavedSession {
  createdAt: number;
  userAgent: string;
  cookieHeader: string;
  bxUa: string;
  bxUmidtoken: string;
  bxV: string;
}

// -------- constructor options --------

/** Options for `new Qwen(...)` and for the second arg of the static helpers. */
export interface QwenOptions {
  /**
   * If true, the instance methods (`.ask`, `.search`, `.image`, `.think`)
   * return async iterators of typed events. If false (default) they return
   * promises of collected results.
   */
  stream?: boolean;

  /** Which Qwen model to use. Defaults to `qwen3.6-plus`. */
  model?: QwenModel;

  /** System prompt — injected on the first turn of a conversation only. */
  system?: string;

  /** Default chat mode for every turn. Defaults to `'normal'`. */
  chatMode?: ChatMode;

  /**
   * Default chat type for plain `ask()` turns. Per-method calls on the
   * instance (like `.search()` and `.image()`) override this for their
   * own turn. Defaults to `'t2t'`.
   */
  chatType?: ChatType;

  /** Enable the thinking phase on all `ask` turns by default. */
  thinking?: boolean;

  // ---- session recovery ----

  /** Existing Qwen-side chat identifier. Pass to resume a previous chat. */
  chatId?: string;

  /** Last response id from a previous turn (used as `parent_id`). */
  lastResponseId?: string;

  /** A `SavedSession` object (e.g. from `chat.exportSession()`) to reuse. */
  session?: SavedSession;

  /** Replay history locally (for `chat.history`). Does not affect the backend. */
  history?: Message[];

  /** Pre-existing usage counters (from a previous `chat.usage`). */
  usage?: QwenUsage;
}

// -------- result shapes (Promise form) --------

export interface TurnUsage {
  input_tokens?: number;
  output_tokens?: number;
  total_tokens?: number;
  characters?: number;
  input_tokens_details?: { text_tokens?: number };
  output_tokens_details?: { text_tokens?: number };
  // image-only:
  width?: number;
  height?: number;
  image_count?: number;
}

export interface AskResult {
  reply: string;
  turn: number;
  usage: TurnUsage | null;
  thinking?: string | null;
}

export interface SearchResult {
  reply: string;
  sources: Source[];
  turn: number;
  usage: TurnUsage | null;
}

export interface ImageResult {
  url: string;
  width: number | null;
  height: number | null;
  model: QwenModel;
  turn: number;
}

export interface ThinkResult {
  reply: string;
  thinking: string | null;
  turn: number;
  usage: TurnUsage | null;
}

// -------- stream events (Stream form) --------

/** One citation returned by the `web_search` tool. */
export interface Source {
  url: string;
  title: string | null;
  snippet: string | null;
  date: string | null;
  hostname: string | null;
}

export type StreamEvent =
  | StartEvent
  | ThinkingEvent
  | TextEvent
  | ToolCallEvent
  | SourcesEvent
  | ImageEvent
  | InfoEvent
  | DoneEvent;

export interface StartEvent {
  type: 'start';
  chatId: string;
  responseId: string | null;
  parentId: string | null;
}

export interface ThinkingEvent {
  type: 'thinking';
  content: string;
  fullThinking: string;
}

export interface TextEvent {
  type: 'text';
  content: string;
  fullContent: string;
  phase?: string;
}

export interface ToolCallEvent {
  type: 'tool_call';
  name: string;
  arguments: string;
  phase: string;
  functionId: string;
}

export interface SourcesEvent {
  type: 'sources';
  sources: Source[];
}

export interface ImageEvent {
  type: 'image';
  url: string;
  width: number | null;
  height: number | null;
  extra?: any;
}

export interface InfoEvent {
  type: 'info';
  info: any;
}

export interface DoneEvent {
  type: 'done';
  reply: string;
  thinking: string | null;
  sources: Source[] | null;
  image: { url: string; width: number | null; height: number | null } | null;
  usage: TurnUsage | null;
  responseId: string | null;
  parentId: string | null;
  turn: number;
  toolEvents: any[];
}

// -------- other shared shapes --------

export interface Message {
  role: 'user' | 'assistant' | 'system' | 'function';
  content: string;
  thinking?: string;
}

/** Accumulated usage counters kept by a `Qwen` chat instance. */
export interface QwenUsage {
  /** Total input tokens consumed across every turn in this chat. */
  input: number;
  /** Total output tokens produced across every turn. */
  output: number;
  /** Grand total (input + output). */
  tokens: number;
  /** Number of turns completed. */
  requests: number;
}

/** Snapshot of a chat's full state — what `chat.toJSON()` returns. */
export interface QwenState {
  chatId: string | null;
  lastResponseId: string | null;
  usage: QwenUsage;
  history: Message[];
  options: QwenOptions;
  session: SavedSession | null;
}
