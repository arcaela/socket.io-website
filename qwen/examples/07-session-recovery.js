// examples/07-session-recovery.js — persist a chat and resume it later.
//
// Use case: a web service receives a user message, needs to continue an
// existing conversation across process restarts or load-balanced workers.
//
//   chat.export()       → returns a flat QwenOptions-compatible blob
//                         (chatId, lastResponseId, history, usage, session,
//                          plus the original constructor options)
//   new Qwen(exported)  → resumes the chat from that blob; the backend
//                         remembers all prior turns via chat_id + parent_id.
//
//   node examples/07-session-recovery.js
const fs = require('node:fs');
const path = require('node:path');
const Qwen = require('..');

const STATE_FILE = path.join(__dirname, 'session-state.json');

(async () => {
  // ── Session 1: create a chat, say a couple of things, export ──
  console.log('--- Session 1: create + export ---');
  const chat1 = new Qwen({ system: 'Sé muy breve.' });
  await chat1.ask('Mi color favorito es azul.');
  await chat1.ask('Mi animal favorito es el lobo.');

  const exported = await chat1.export();
  console.log('exported:');
  console.log('  chatId:        ', exported.chatId);
  console.log('  history:       ', exported.history.length, 'entries');
  console.log('  usage.tokens:  ', exported.usage.tokens);
  console.log('  usage.rounds:  ', exported.usage.rounds);
  if (exported.session) {
    console.log('  session age:   ', Math.round((Date.now() - exported.session.createdAt) / 1000), 's');
  }

  fs.writeFileSync(STATE_FILE, JSON.stringify(exported, null, 2));
  console.log(`saved to ${STATE_FILE}`);

  // ── Session 2: simulate a restart, load from disk, hand to new Qwen() ──
  console.log('\n--- Session 2: recover ---');
  const saved = JSON.parse(fs.readFileSync(STATE_FILE, 'utf8'));

  // Direct use — no spread, no destructuring. `saved` IS the options blob.
  const chat2 = new Qwen(saved);
  console.log('chat2 resumed with:');
  console.log('  chatId:        ', chat2.chatId);
  console.log('  history:       ', chat2.history.length, 'entries');
  console.log('  usage.rounds:  ', chat2.usage.rounds);

  // Memory test — the backend remembers everything from session 1
  const r = await chat2.ask('¿Cuál es mi color favorito y cuál es mi animal favorito?');
  console.log('\nrecall test →', r.reply);
  console.log('usage after:', chat2.usage);

  fs.unlinkSync(STATE_FILE);
})().catch((e) => {
  if (e instanceof Qwen.QwenBadRequestError && /not exist/i.test(e.message)) {
    console.error('recovery failed — the chat_id has expired server-side');
    console.error('in production, catch this and fall back to creating a new chat');
  } else {
    console.error('error:', e.constructor.name, e.message);
  }
  process.exit(1);
});
