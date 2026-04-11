// examples/06-errors.js — container startup + typed error handling.
//
// Key points:
//   1. qwen.warmup() pre-initializes the session so the first real request
//      doesn't pay the ~15s jsdom+umid boot cost.
//   2. Errors are typed exceptions. Both the Promise form (via await) and
//      the Stream form (via for-await) throw the SAME exception classes.
//      Nothing is ever returned as an "error chunk".
//   3. qwen.ask / search / image / think auto-rotate the session on
//      RateLimited / Unauthorized and retry once. You only see the error
//      if the retry also fails.
//
//   node examples/06-errors.js
const qwen = require('../src');

(async () => {
  // ── 1. Warmup ──
  const t0 = Date.now();
  const s = await qwen.warmup();
  console.log(
    `[warmup] ${Date.now() - t0}ms, session expires in ${Math.round(s.expiresIn / 1000)}s`
  );

  // ── 2. Promise-form error handling ──
  try {
    const reply = await qwen.ask('Decime solo: funciona');
    console.log('[ask promise]', reply);
  } catch (e) {
    handleQwenError(e, 'ask');
  }

  // ── 3. Stream-form error handling (same exception types) ──
  try {
    for await (const ev of qwen.ask.stream('Decime solo: stream')) {
      if (ev.type === 'text') process.stdout.write(ev.content);
      if (ev.type === 'done') process.stdout.write('\n');
    }
  } catch (e) {
    handleQwenError(e, 'ask.stream');
  }

  // ── 4. Distinguishing error types ──
  console.log('\nerror classes exposed:');
  for (const k of ['QwenError', 'QwenRateLimitedError', 'QwenUnauthorizedError',
                   'QwenBadRequestError', 'QwenServerError', 'QwenNetworkError']) {
    console.log(`  qwen.${k}:`, typeof qwen[k] === 'function' ? '✓' : '✗');
  }
})().catch((e) => { console.error('fatal:', e.constructor.name, e.message); process.exit(1); });

function handleQwenError(e, where) {
  // Either `instanceof` or `.code` works; pick whichever is more ergonomic.
  if (e instanceof qwen.QwenRateLimitedError) {
    console.error(`[${where}] RATE LIMITED — retry in ${e.retryAfterHours}h`);
    // Manual recovery: rotate the session to a fresh device token
    return qwen.warmup({ forceRefresh: true });
  }
  if (e instanceof qwen.QwenUnauthorizedError) {
    console.error(`[${where}] UNAUTHORIZED — tokens expired. Rotating.`);
    return qwen.warmup({ forceRefresh: true });
  }
  if (e instanceof qwen.QwenBadRequestError) {
    console.error(`[${where}] BAD REQUEST — client bug: ${e.message}`);
    return; // usually not recoverable; fix the caller
  }
  if (e instanceof qwen.QwenServerError) {
    console.error(`[${where}] SERVER ERROR (typically an unsupported feature)`);
    return;
  }
  if (e instanceof qwen.QwenNetworkError) {
    console.error(`[${where}] NETWORK — ${e.message}`);
    return;
  }
  // Any other QwenError subclass
  if (e instanceof qwen.QwenError) {
    console.error(`[${where}] QwenError (${e.code}): ${e.message}`);
    return;
  }
  // Not a Qwen error at all — rethrow
  throw e;
}
