// examples/01-ask.js — plain text Q&A in all three forms.
//
//   node examples/01-ask.js
const Qwen = require('..');  // resolves to dist/index.js via package.json main

(async () => {
  // ── Form A: static shortcut ── (creates a throwaway instance)
  console.log('--- Qwen.ask (static) ---');
  const reply = await Qwen.ask('¿Cuál es la capital de Argentina? Una palabra.');
  console.log('reply:', reply);

  // ── Form B: instance, promise mode (default) ──
  console.log('\n--- new Qwen() — promise ---');
  const chat = new Qwen({ system: 'Respondé en ruso corto.' });
  const r1 = await chat.ask('hola');
  console.log('reply:', r1.reply);
  console.log('turn:', r1.turn, 'usage:', chat.usage);

  // ── Form C: instance, stream mode ──
  console.log('\n--- new Qwen({ stream: true }) ---');
  const streaming = new Qwen({ stream: true });
  process.stdout.write('chunks: ');
  for await (const ev of streaming.ask('Contá del 1 al 5 separados por guiones.')) {
    if (ev.type === 'text') process.stdout.write(ev.content);
    if (ev.type === 'done') console.log(`\n[done] usage=${streaming.usage.tokens.total}t rounds=${streaming.usage.rounds}`);
  }

  // ── Typed error handling (same exceptions for both forms) ──
  try {
    await Qwen.ask('...');
  } catch (e) {
    if (e instanceof Qwen.QwenRateLimitedError) {
      console.error('rate limited, retry in', e.retryAfterHours, 'hours');
    } else if (e instanceof Qwen.QwenError) {
      console.error('qwen error', e.code, ':', e.message);
    } else {
      throw e;
    }
  }
})().catch((e) => { console.error('error:', e.constructor.name, e.message); process.exit(1); });
