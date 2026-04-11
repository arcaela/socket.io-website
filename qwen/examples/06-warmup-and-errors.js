// examples/06-warmup-and-errors.js — container-friendly startup + error handling.
//
// Shows:
//   1. How to pre-warm the session on process start (so the first user
//      request doesn't pay the ~15 s jsdom+umid boot cost).
//   2. How to detect and recover from the RateLimited error code by
//      rotating the session.
//
//   node examples/06-warmup-and-errors.js
const qwen = require('../src');

(async () => {
  // 1. Warm the session on startup. Call this right after require() in any
  //    long-running process — it's a no-op if the cache is still valid.
  const t0 = Date.now();
  const s = await qwen.warmup();
  console.log(
    `[warmup] done in ${Date.now() - t0} ms, ` +
    `session expires in ${Math.round(s.expiresIn / 1000)} s`
  );

  // 2. Make a request with explicit error handling.
  try {
    const reply = await qwen.ask('Decime solo: funciona');
    console.log(`[ask] reply: ${reply}`);
  } catch (e) {
    // Structured error fields populated from the non-SSE JSON envelope
    console.error('[ask] ERROR');
    console.error('  message:         ', e.message);
    console.error('  .code:           ', e.code);           // "RateLimited"
    console.error('  .status:         ', e.status);         // HTTP code
    console.error('  .retryAfterHours:', e.retryAfterHours);
    console.error('  .response:       ', e.response);       // full envelope

    // Recover by rotating the session (new um.init → new bx-umidtoken → new
    // guest quota for a fresh "device"). qwen() already does this
    // automatically when retryOnAuthFail is on (the default), but you can
    // also do it explicitly like this for long-lived processes.
    if (e.code === 'RateLimited') {
      console.log('[recovery] rotating session...');
      await qwen.warmup({ forceRefresh: true });
      const reply = await qwen.ask('Decime solo: funciona');
      console.log(`[recovery] reply: ${reply}`);
    }
  }
})().catch((e) => { console.error('fatal:', e.message); process.exit(1); });
