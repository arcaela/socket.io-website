// examples/06-errors.js — container startup + typed error handling.
//
// Key points:
//   1. Qwen.warmup() pre-initializes the session so the first real request
//      doesn't pay the ~15s jsdom+umid boot cost.
//   2. Errors are typed exceptions. Both promise and stream forms throw
//      the SAME exception classes. Nothing is ever returned as an
//      "error chunk".
//
//   node examples/06-errors.js
const Qwen = require('..');

(async () => {
  // Warmup — fast-path no-op if a cached session is still valid
  const t0 = Date.now();
  const s = await Qwen.warmup();
  console.log(
    `[warmup] ${Date.now() - t0}ms, session expires in ${Math.round(s.expiresIn / 1000)}s`
  );

  // Promise-form error handling
  try {
    const reply = await Qwen.ask('Decime solo: funciona');
    console.log('[Qwen.ask]', reply);
  } catch (e) {
    handle(e, 'Qwen.ask');
  }

  // Stream-form error handling (same exception types)
  try {
    const chat = new Qwen({ stream: true });
    for await (const ev of chat.ask('Decime solo: stream')) {
      if (ev.type === 'text') process.stdout.write(ev.content);
      if (ev.type === 'done') process.stdout.write('\n');
    }
  } catch (e) {
    handle(e, 'chat.ask.stream');
  }

  // List all error classes exposed as static members
  console.log('\nerror classes exposed on Qwen:');
  for (const k of ['QwenError', 'QwenRateLimitedError', 'QwenUnauthorizedError',
                   'QwenBadRequestError', 'QwenServerError', 'QwenNetworkError']) {
    console.log(`  Qwen.${k}:`, typeof Qwen[k] === 'function' ? '✓' : '✗');
  }
})().catch((e) => { console.error('fatal:', e.constructor.name, e.message); process.exit(1); });

function handle(e, where) {
  if (e instanceof Qwen.QwenRateLimitedError) {
    console.error(`[${where}] RATE LIMITED — retry in ${e.retryAfterHours}h`);
    return Qwen.warmup({ forceRefresh: true });
  }
  if (e instanceof Qwen.QwenUnauthorizedError) {
    console.error(`[${where}] UNAUTHORIZED — rotating session`);
    return Qwen.warmup({ forceRefresh: true });
  }
  if (e instanceof Qwen.QwenBadRequestError) {
    console.error(`[${where}] BAD REQUEST — client bug: ${e.message}`);
    return;
  }
  if (e instanceof Qwen.QwenServerError) {
    console.error(`[${where}] SERVER ERROR — unsupported feature?`);
    return;
  }
  if (e instanceof Qwen.QwenNetworkError) {
    console.error(`[${where}] NETWORK — ${e.message}`);
    return;
  }
  if (e instanceof Qwen.QwenError) {
    console.error(`[${where}] QwenError (${e.code}): ${e.message}`);
    return;
  }
  throw e;
}
