// src/lib/constants.ts
// All hard-coded pieces of the Qwen guest protocol in one place.
import * as path from 'node:path';
import type { QwenModel } from './types';

export const BASE = 'https://chat.qwen.ai';
export const API = `${BASE}/api/v2`;

export const SPA_VERSION = '0.2.37';
export const BX_V = '2.5.36';
export const DEFAULT_MODEL: QwenModel = 'qwen3.6-plus';

/** Known models that the guest endpoint currently accepts. */
export const KNOWN_MODELS = [
  'qwen3.6-plus',
  'qwen3.5-plus',
  'qwen3.5-omni-plus',
] as const satisfies readonly QwenModel[];

export const USER_AGENT =
  'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) ' +
  'Chrome/147.0.0.0 Safari/537.36';

/** AWSC bundle URLs, pinned to specific versions for a stable fingerprint. */
export const AWSC_URLS = {
  awsc: 'https://g.alicdn.com/AWSC/AWSC/awsc.js',
  collina: 'https://g.alicdn.com/AWSC/uab/1.140.0/collina.js',
  um: 'https://g.alicdn.com/AWSC/WebUMID/1.93.0/um.js',
} as const;

/** Endpoint driven by um.js during initialization to mint a bx-umidtoken. */
export const UMID_REGISTER_URL = 'https://ynuf.aliapp.org/service/um.json';

/** Writable directory where the session + AWSC scripts are cached. */
export const CACHE_DIR: string =
  process.env.QWEN_CACHE_DIR ||
  path.join(process.env.HOME || '/tmp', '.cache', 'qwen-node');

export const SESSION_FILE: string = path.join(CACHE_DIR, 'session.json');
export const AWSC_DIR: string = path.join(CACHE_DIR, 'awsc');

/** `acw_tc` expires after 30 minutes; refresh a bit earlier to be safe. */
export const SESSION_TTL_MS = 25 * 60 * 1000;
