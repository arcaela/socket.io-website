// examples/05-chat.js — stateful multi-turn conversation, mixing all four
// methods in a single chat and showing both Promise and Stream forms.
//
// `qwen.chat()` is the primitive. The returned handle has four turn methods,
// each with a `.stream` property that exposes the same underlying iterator.
//
//   chat.ask(msg)          ← plain text turn
//   chat.search(q)         ← this turn uses web search
//   chat.image(p)          ← this turn generates an image
//   chat.think(msg)        ← this turn enables thinking
//
//   chat.ask.stream(msg)   ← streaming version of each (async iterator)
//   chat.search.stream(q)
//   chat.image.stream(p)
//   chat.think.stream(msg)
//
//   node examples/05-chat.js
const qwen = require('../src');

(async () => {
  const chat = await qwen.chat({
    model: 'qwen3.6-plus',
    system: 'Sos un asistente argentino muy breve. Menos de 25 palabras.',
  });
  console.log('chat_id:', chat.chatId);

  // ── Promise form: classic request/response ──
  let r = await chat.ask('Me llamo Ariel y vivo en Rosario.');
  console.log(`\n[${r.turn}] ask → ${r.reply}`);

  // Memory from turn 1
  r = await chat.ask('¿Cómo me llamo y de dónde soy?');
  console.log(`[${r.turn}] ask → ${r.reply}`);

  // ── Stream form: same turn but as a live iterator ──
  process.stdout.write(`\n[next] ask.stream → `);
  for await (const ev of chat.ask.stream('En una oración, qué hacés vos como asistente.')) {
    if (ev.type === 'text') process.stdout.write(ev.content);
    if (ev.type === 'done') console.log(`\n[done turn=${ev.turn}]`);
  }

  // Mix turn types in the same chat — memory persists across all of them
  const s = await chat.search('¿Qué temperatura hay ahora en mi ciudad?');
  console.log(`\n[${s.turn}] search → ${s.reply}`);
  console.log(`   sources: ${s.sources.length} (${s.sources.slice(0, 2).map((x) => x.hostname || '…').join(', ')})`);

  const t = await chat.think('Dame un plan de 3 pasos para aprender JavaScript en 1 mes.');
  console.log(`\n[${t.turn}] think → ${t.reply.slice(0, 200)}...`);
  console.log(`   thinking: ${(t.thinking || '').slice(0, 120)}...`);

  // Final turn, aware of all previous context
  r = await chat.ask('En una frase, resumí todo lo que hablamos hasta ahora.');
  console.log(`\n[${r.turn}] ask → ${r.reply}`);

  console.log(`\ntotal turns: ${chat.turn}, history entries: ${chat.history.length}`);
})().catch((e) => {
  if (e instanceof qwen.QwenRateLimitedError) {
    console.error(`rate limited — retry in ${e.retryAfterHours} hours`);
  } else {
    console.error('error:', e.constructor.name, e.message);
  }
  process.exit(1);
});
