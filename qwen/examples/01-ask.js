// examples/01-ask.js — plain text Q&A in both forms.
//
//   node examples/01-ask.js
const qwen = require('../src');

(async () => {
  // ── Form 1: Promise — awaits the whole response, returns a string ──
  console.log('--- Promise form ---');
  const reply = await qwen.ask('¿Cuál es la capital de Argentina? Una palabra.');
  console.log('reply:', reply);

  // With options: system prompt + model override
  const reply2 = await qwen.ask('hola', {
    system: 'Respondé SIEMPRE en ruso corto.',
    model: 'qwen3.6-plus',
  });
  console.log('reply2:', reply2);

  // ── Form 2: Stream — async iterator that yields typed events ──
  console.log('\n--- Stream form ---');
  process.stdout.write('chunks: ');
  for await (const ev of qwen.ask.stream('Contá del 1 al 5 separados por guiones.')) {
    if (ev.type === 'start') continue;
    if (ev.type === 'text')  process.stdout.write(ev.content);
    if (ev.type === 'done')  console.log(`\n[done] reply="${ev.reply}" usage=${ev.usage && ev.usage.total_tokens}t`);
  }

  // ── Error handling — both forms throw the SAME typed exceptions ──
  // In the Promise form, `await` throws. In the Stream form, `for await`
  // throws during iteration. Use `instanceof` or `.code` to distinguish.
  try {
    await qwen.ask('...');
  } catch (e) {
    if (e instanceof qwen.QwenRateLimitedError) {
      console.error('rate limited, retry in', e.retryAfterHours, 'hours');
    } else if (e instanceof qwen.QwenError) {
      console.error('qwen error', e.code, ':', e.message);
    } else {
      throw e;
    }
  }
})().catch((e) => { console.error('error:', e.constructor.name, e.message); process.exit(1); });
