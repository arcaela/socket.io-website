// examples/04-think.js — thinking mode in both forms.
//
// Qwen emits its internal chain-of-thought as deltas tagged with
// `phase: "think"` BEFORE the final answer (`phase: "answer"`).
//
// Promise form: returns { reply, thinking, usage }
// Stream form:  yields
//   { type: 'thinking', content, fullThinking }
//   { type: 'text',     content, fullContent }
//   { type: 'done',     reply, thinking, usage }
//
//   node examples/04-think.js
const qwen = require('../src');

(async () => {
  // ── Promise form ──
  console.log('--- Promise form ---');
  const { reply, thinking } = await qwen.think(
    'Un tren sale de Buenos Aires a 80 km/h hacia Mar del Plata (400 km). ' +
    'Otro sale de Mar del Plata a 60 km/h al mismo tiempo, en dirección contraria. ' +
    '¿Después de cuántas horas se cruzan?'
  );
  console.log('\n=== thinking ===');
  console.log(thinking ? thinking.slice(0, 500) + (thinking.length > 500 ? '\n[...truncado]' : '') : '(vacío)');
  console.log('\n=== answer ===');
  console.log(reply);

  // ── Stream form — separately stream thinking and answer ──
  console.log('\n\n--- Stream form ---');
  await qwen.warmup({ forceRefresh: true });
  let currentPhase = null;
  for await (const ev of qwen.think.stream('¿Cuánto es 15 × 17? Explicalo en pasos cortos.')) {
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
      console.log('\n\n[done]', ev.usage && ev.usage.total_tokens, 'tokens');
    }
  }
})().catch((e) => {
  if (e instanceof qwen.QwenRateLimitedError) {
    console.error(`rate limited — retry in ${e.retryAfterHours} hours`);
  } else {
    console.error('error:', e.constructor.name, e.message);
  }
  process.exit(1);
});
