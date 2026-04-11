// src/lib/session.ts
// Session cache + lifecycle.
//
// A session is:
//   { createdAt, userAgent, cookieHeader, bxUa, bxUmidtoken, bxV }
//
// The in-memory state can be overridden via `injectSession(saved)` so that
// callers who persisted a previous session can replay it without hitting
// the token-acquisition path again.
import * as fs from 'node:fs';
import {
  CACHE_DIR,
  SESSION_FILE,
  SESSION_TTL_MS,
  USER_AGENT,
  BX_V,
} from './constants';
import { acquireTokens } from './tokens';
import { fetchFreshCookies } from './cookies';
import type { SavedSession } from './types';
import type { JsdomEnv } from './jsdom-env';

function ensureDir(p: string): void {
  fs.mkdirSync(p, { recursive: true });
}

export function loadCachedSession(): SavedSession | null {
  try {
    if (!fs.existsSync(SESSION_FILE)) return null;
    const s = JSON.parse(fs.readFileSync(SESSION_FILE, 'utf8'));
    if (!s || !s.bxUa || !s.bxUmidtoken || !s.cookieHeader) return null;
    if (Date.now() - (s.createdAt || 0) > SESSION_TTL_MS) return null;
    return s as SavedSession;
  } catch {
    return null;
  }
}

export function saveSession(s: SavedSession): void {
  ensureDir(CACHE_DIR);
  fs.writeFileSync(SESSION_FILE, JSON.stringify(s, null, 2));
}

/** Write an externally-supplied session into the on-disk cache. */
export function injectSession(s: SavedSession): void {
  saveSession(s);
}

export interface GetSessionOptions {
  forceRefresh?: boolean;
}

export type GetSessionFn = (opts?: GetSessionOptions) => Promise<SavedSession>;

export interface MakeGetSessionDeps {
  ensureJsdomEnv: () => JsdomEnv;
}

export function makeGetSession({ ensureJsdomEnv }: MakeGetSessionDeps): GetSessionFn {
  let _inFlight: Promise<SavedSession> | null = null;

  return async function getSession({ forceRefresh = false }: GetSessionOptions = {}): Promise<SavedSession> {
    if (!forceRefresh) {
      const cached = loadCachedSession();
      if (cached) return cached;
    }
    if (_inFlight) return _inFlight;

    _inFlight = (async () => {
      const env = ensureJsdomEnv();
      const { bxUa, umidToken } = await acquireTokens(env);
      const cookieHeader = await fetchFreshCookies();
      const session: SavedSession = {
        createdAt: Date.now(),
        userAgent: USER_AGENT,
        cookieHeader,
        bxUa,
        bxUmidtoken: umidToken,
        bxV: BX_V,
      };
      saveSession(session);
      return session;
    })();

    try {
      return await _inFlight;
    } finally {
      _inFlight = null;
    }
  };
}
