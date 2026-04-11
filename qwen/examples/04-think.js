// examples/04-think.js — thinking mode, promise + stream forms.
//
//   node examples/04-think.js
const Qwen = require('..');

(async () => {
  // ── static shortcut ──
  console.log('--- Qwen.think (static) ---');
  const { reply, thinking } = await Qwen.think(
    'Un tren sale de Buenos Aires a 80 km/h hacia Mar del Plata (400 km). ' +
    'Otro sale de Mar del Plata a 60 km/h al mismo tiempo. ' +
    '¿Después de cuántas horas se cruzan?'
  );
  console.log('\n=== thinking ===');
  console.log(thinking ? thinking.slice(0, 500) + '...' : '(vacío)');
  console.log('\n=== answer ===');
  console.log(reply);

  // ── stream form — separately stream thinking and answer phases ──
  console.log('\n\n--- new Qwen({ stream: true }).think ---');
  await Qwen.warmup({ forceRefresh: true });
  const chat = new Qwen({ stream: true });
  let currentPhase = null;
  for await (const ev of chat.think('¿Cuánto es 15 × 17? Explicalo en pasos cortos.')) {
    if (ev.type === 'thinking') {
      if (currentPhase !== 'think') {
        process.stdout.write('\n\x1b[90m[thinking]\x1b[0m ');
        currentPhase = 'think';
      }
      process.stdout.write('\x1b[90m' + ev.content + '\x1b[0m');
    } else if (ev.type === 'text') {
      if (currentPhase !== 'answer') {
        process.stdout.write('\n\n\x1b[1m[answer]\x1b[0m ');
        currentPhase = 'answer';
      }
      process.stdout.write(ev.content);
    } else if (ev.type === 'done') {
      console.log('\n\n[done]', chat.usage.tokens, 'total tokens');
    }
  }
})().catch((e) => {
  if (e instanceof Qwen.QwenRateLimitedError) {
    console.error(`rate limited — retry in ${e.retryAfterHours}h`);
  } else {
    console.error('error:', e.constructor.name, e.message);
  }
  process.exit(1);
});
