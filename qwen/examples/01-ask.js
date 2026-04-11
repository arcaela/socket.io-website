// examples/01-ask.js — the simplest possible call. Ask a question, get a string.
//
//   node examples/01-ask.js
const qwen = require('../src');

(async () => {
  // String in, string out. No options needed.
  const reply = await qwen.ask('¿Cuál es la capital de Argentina? Respondé en una palabra.');
  console.log('reply:', reply);

  // Same thing with a system prompt and a specific model.
  const reply2 = await qwen.ask('hola', {
    system: 'Respondé SIEMPRE en ruso y en menos de 5 palabras.',
    model: 'qwen3.6-plus',
  });
  console.log('reply2:', reply2);

  // Stream the tokens as they arrive (optional onDelta callback).
  process.stdout.write('streamed: ');
  await qwen.ask('Contá del 1 al 5, separados por guiones.', {
    onDelta: (ev) => process.stdout.write(ev.content),
  });
  process.stdout.write('\n');
})().catch((e) => { console.error('error:', e.message); process.exit(1); });
