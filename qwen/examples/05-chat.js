// examples/05-chat.js — stateful multi-turn conversation.
//
// `qwen.chat()` is THE primitive. Every one-shot helper (qwen.ask, qwen.search,
// qwen.image, qwen.think) is implemented as a throwaway call to this — so
// `chat` is the "real" surface and the top-level shortcuts exist only for
// the 80% case where you don't need memory.
//
// The handle exposes four turn methods, each maps 1:1 to a one-shot helper:
//
//   chat.ask(msg)      ← plain text turn  (uses the chat's default chatType)
//   chat.search(msg)   ← this turn uses web search (and returns .sources)
//   chat.image(msg)    ← this turn generates an image
//   chat.think(msg)    ← this turn enables thinking (returns .thinking + .reply)
//
// All four can be freely mixed inside the same chat. Qwen's backend keeps
// the conversation memory server-side so every turn sees the context of
// all previous turns regardless of type.
//
//   node examples/05-chat.js
const qwen = require('../src');

(async () => {
  const chat = await qwen.chat({
    model: 'qwen3.6-plus',
    system: 'Sos un asistente argentino muy breve. Menos de 25 palabras.',
  });
  console.log('chat_id:', chat.chatId);

  // Turn 1 — plain text
  let r = await chat.ask('Me llamo Ariel y vivo en Rosario.');
  console.log(`\n[${r.turn}] ask: ${r.reply}`);

  // Turn 2 — plain text with memory from turn 1
  r = await chat.ask('¿Cómo me llamo y de dónde soy?');
  console.log(`[${r.turn}] ask: ${r.reply}`);

  // Turn 3 — switch to web search just for this turn
  const s = await chat.search('¿Qué temperatura hay hoy en mi ciudad?');
  console.log(`\n[${s.turn}] search: ${s.reply}`);
  console.log('  sources:');
  for (const src of s.sources.slice(0, 3)) {
    console.log(`    - ${(src.title || src.url).slice(0, 80)}`);
  }

  // Turn 4 — think + plan, still with full memory of turns 1-3
  const t = await chat.think(
    'Diseñá un mini plan de viaje de fin de semana a la costa, paso a paso.'
  );
  console.log(`\n[${t.turn}] think answer: ${t.reply.slice(0, 300)}...`);
  if (t.thinking) {
    console.log(`        thinking: ${t.thinking.slice(0, 150)}...`);
  }

  // Turn 5 — back to plain text, knows everything from 1-4
  r = await chat.ask('En una frase, resumí lo que sabés de mí y lo que acabamos de hablar.');
  console.log(`\n[${r.turn}] ask: ${r.reply}`);

  console.log(`\ntotal turns: ${chat.turn}, history entries: ${chat.history.length}`);
})().catch((e) => {
  console.error('error:', e.message);
  if (e.code) console.error('code:', e.code, 'retryAfterHours:', e.retryAfterHours);
  process.exit(1);
});
