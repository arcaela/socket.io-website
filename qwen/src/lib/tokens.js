// src/lib/tokens.js
// Acquires a fresh bx-ua + bx-umidtoken pair by driving AWSC inside the
// pre-built jsdom window. This is always async because um.init() does a
// round-trip to https://ynuf.aliapp.org/service/um.json to receive the
// umid token, and we have to poll the xhrLog to read the response.
'use strict';

const { UMID_REGISTER_URL } = require('./constants');

async function acquireTokens(env) {
  const { window, xhrLog } = env;

  // Clear any stale entries from previous invocations in the same process.
  // This matters when acquireTokens is called multiple times (e.g. for
  // forceRefresh after a rate limit) against the same jsdom instance.
  xhrLog.length = 0;

  // ⚠️ EVENT-LOOP YIELD
  // Without this `setImmediate` yield the fetch() launched from inside
  // NodeXHR.prototype.send silently fails with "fetch failed" because
  // undici's global dispatcher hasn't finished initializing yet. This
  // only happens when the caller reaches us from a long promise chain
  // (e.g. require('./index') returns → caller calls qwen() → down the
  // async stack into here). A single setImmediate resolves it.
  await new Promise((r) => setImmediate(r));

  // AWSC's own internal initialization is a few hundred ms of setTimeout
  // chains. Wait for it to settle before we trigger um.init, otherwise
  // `window.AWSC.use('um', cb)` resolves before the module is fully
  // registered and we get a half-built facade.
  await new Promise((r) => setTimeout(r, 1500));

  // 1. bx-umidtoken: drive `um.init` which POSTs device fingerprint data
  //    and receives {"tn": "<token>", "id": "<token>"}.
  let umMod = null;
  window.AWSC.use('um', (_, mod) => { umMod = mod; });
  if (!umMod || typeof umMod.init !== 'function') {
    throw new Error('AWSC umid module not available after setup');
  }
  try {
    umMod.init({
      appName: 'baxia',
      appKey: 'chat.qwen.ai',
      // `getToken` is a user-supplied callback that um.js uses to attach
      // an app-level token to its registration payload. We feed it a
      // placeholder because there is none for a guest session.
      getToken: (cb) => { if (typeof cb === 'function') cb('test-token'); },
    });
  } catch (e) {
    throw new Error(`um.init threw: ${e.message}`);
  }

  // 2. Poll xhrLog for the umid registration response. It lands within
  //    ~500-1500 ms of um.init() on a warm JVM/undici.
  let umidToken = null;
  const deadline = Date.now() + 12000;
  while (!umidToken && Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 150));
    for (const entry of xhrLog) {
      if (!entry.responseText || !entry.url) continue;
      if (!entry.url.includes('ynuf.aliapp.org')) continue;
      try {
        const j = JSON.parse(entry.responseText);
        if (j && j.tn) { umidToken = j.tn; break; }
      } catch (_) {}
    }
  }
  if (!umidToken) {
    throw new Error(
      `umid registration did not return a bx-umidtoken ` +
      `(is ${UMID_REGISTER_URL} reachable from this pod?)`
    );
  }

  // 3. bx-ua is synchronous — collina.js computes it from the browser
  //    environment on `getUA()`. It's the ~584-char "140#..." token.
  let uabMod = null;
  window.AWSC.use('uab', (_, mod) => { uabMod = mod; });
  if (!uabMod || typeof uabMod.getUA !== 'function') {
    throw new Error('AWSC uab module not available');
  }
  const bxUa = uabMod.getUA();
  if (typeof bxUa !== 'string' || bxUa.length < 100) {
    throw new Error(`unexpected bx-ua format (length ${bxUa && bxUa.length}): ${bxUa}`);
  }

  return { bxUa, umidToken };
}

module.exports = { acquireTokens };
