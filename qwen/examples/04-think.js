// examples/04-think.js — thinking mode.
//
// `qwen.think()` activates Qwen's chain-of-thought phase. The model emits
// its reasoning as a separate `phase: "think"` before the final answer.
// The helper returns both { reply, thinking }.
//
//   node examples/04-think.js
const qwen = require('../src');

(async () => {
  const { reply, thinking, usage } = await qwen.think(
    'Un tren sale de Buenos Aires a 80 km/h hacia Mar del Plata (400 km). ' +
    'Otro sale de Mar del Plata a 60 km/h al mismo tiempo, en dirección contraria. ' +
    '¿Después de cuántas horas se cruzan? Resolvé paso a paso.'
  );

  console.log('=== thinking ===');
  console.log(thinking ? thinking.slice(0, 800) + (thinking.length > 800 ? '\n[...truncado]' : '') : '(vacío)');

  console.log('\n=== answer ===');
  console.log(reply);

  console.log('\nusage:', usage);
})().catch((e) => { console.error('error:', e.message); process.exit(1); });
