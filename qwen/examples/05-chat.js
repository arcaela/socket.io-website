// examples/05-chat.js — stateful multi-turn chat, mixing all four methods.
//
//   node examples/05-chat.js
const Qwen = require('..');

(async () => {
  const chat = new Qwen({
    model: 'qwen3.6-plus',
    system: 'Sos un asistente argentino muy breve. Menos de 25 palabras.',
  });
  console.log('model:', chat.options.model);

  // Turn 1 — plain ask
  let r = await chat.ask('Me llamo Ariel y vivo en Rosario.');
  console.log(`\n[turn ${r.turn}] ask → ${r.reply}`);
  console.log(`  chat_id: ${chat.chatId}`);

  // Turn 2 — memory from turn 1
  r = await chat.ask('¿Cómo me llamo y de dónde soy?');
  console.log(`[turn ${r.turn}] ask → ${r.reply}`);

  // Turn 3 — search within the same chat
  const s = await chat.search('¿Qué temperatura hay ahora en mi ciudad?');
  console.log(`\n[turn ${s.turn}] search → ${s.reply}`);
  console.log(`   sources: ${s.sources.length}`);

  // Turn 4 — think + plan (still remembers everything)
  const t = await chat.think('Dame un plan de 3 pasos para aprender JavaScript en 1 mes.');
  console.log(`\n[turn ${t.turn}] think → ${t.reply.slice(0, 200)}...`);
  if (t.thinking) console.log(`   thinking: ${t.thinking.slice(0, 120)}...`);

  // Final turn — recall everything
  r = await chat.ask('En una frase, resumí lo que hablamos.');
  console.log(`\n[turn ${r.turn}] ask → ${r.reply}`);

  console.log(`\ntotal turns: ${chat.usage.requests}`);
  console.log(`tokens: ${chat.usage.tokens} (in=${chat.usage.input}, out=${chat.usage.output})`);
  console.log(`history entries: ${chat.history.length}`);
})().catch((e) => {
  if (e instanceof Qwen.QwenRateLimitedError) {
    console.error(`rate limited — retry in ${e.retryAfterHours}h`);
  } else {
    console.error('error:', e.constructor.name, e.message);
  }
  process.exit(1);
});
