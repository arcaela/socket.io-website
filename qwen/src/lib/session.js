// src/lib/session.js
// Session = { createdAt, userAgent, cookieHeader, bxUa, bxUmidtoken, bxV }.
//
// We persist sessions to disk (via CACHE_DIR) so that across process
// restarts we don't redo the jsdom + umid-server dance every single time.
// TTL matches the acw_tc lifetime (~25 minutes, leaving 5-minute margin).
'use strict';

const fs = require('node:fs');
const {
  CACHE_DIR,
  SESSION_FILE,
  SESSION_TTL_MS,
  USER_AGENT,
  BX_V,
} = require('./constants');
const { acquireTokens } = require('./tokens');
const { fetchFreshCookies } = require('./cookies');

function ensureDir(p) { fs.mkdirSync(p, { recursive: true }); }

function loadCachedSession() {
  try {
    if (!fs.existsSync(SESSION_FILE)) return null;
    const s = JSON.parse(fs.readFileSync(SESSION_FILE, 'utf8'));
    if (!s || !s.bxUa || !s.bxUmidtoken || !s.cookieHeader) return null;
    if (Date.now() - (s.createdAt || 0) > SESSION_TTL_MS) return null;
    return s;
  } catch (_) { return null; }
}

function saveSession(s) {
  ensureDir(CACHE_DIR);
  fs.writeFileSync(SESSION_FILE, JSON.stringify(s, null, 2));
}

// Factory that returns getSession(). Takes an `ensureJsdomEnv` callback so
// we don't have a module-level import of jsdom-env (which would force the
// ~150 ms jsdom require even on fast paths that never need to build tokens).
function makeGetSession({ ensureJsdomEnv }) {
  let _acquireInFlight = null;

  return async function getSession({ forceRefresh = false } = {}) {
    if (!forceRefresh) {
      const cached = loadCachedSession();
      if (cached) return cached;
    }
    // Coalesce concurrent callers so we only run one token-acquisition at a
    // time per process. If two requests come in at exactly the same
    // cold-start instant, they share the same promise.
    if (_acquireInFlight) return _acquireInFlight;

    _acquireInFlight = (async () => {
      const env = ensureJsdomEnv();
      const { bxUa, umidToken } = await acquireTokens(env);
      const cookieHeader = await fetchFreshCookies();
      const session = {
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

    try { return await _acquireInFlight; }
    finally { _acquireInFlight = null; }
  };
}

module.exports = { loadCachedSession, saveSession, makeGetSession };
