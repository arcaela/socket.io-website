// examples/07-session-recovery.js — persist a chat and resume it later.
//
// Use case: user closes the tab, your service needs to rehydrate the
// same Qwen conversation (with memory) on the next request, possibly
// from a different process entirely.
//
// Snapshot your chat with `await chat.exportSession()`, persist it
// (Redis, DB, disk), and later pass the snapshot to `new Qwen(...)`.
// Caveats:
//   - The backend chat_id only stays valid as long as the underlying
//     session credentials do (~25 minutes for the acw_tc cookie).
//   - If the session is too old, pass `session: null` in the recovery
//     options and the library will mint a fresh one (but Qwen will
//     reject old chat_ids — you'll get a Bad_Request).
//
//   node examples/07-session-recovery.js
const fs = require('node:fs');
const path = require('node:path');
const Qwen = require('..');

const STATE_FILE = path.join(__dirname, 'session-state.json');

(async () => {
  // ── Session 1: create a chat, say a couple of things, export state ──
  console.log('--- Session 1: create + export ---');
  const chat1 = new Qwen({ system: 'Sé muy breve.' });
  await chat1.ask('Mi color favorito es azul.');
  await chat1.ask('Mi animal favorito es el lobo.');

  const state = await chat1.exportSession();
  console.log('exported:');
  console.log('  chatId:     ', state.chatId);
  console.log('  history:    ', state.history.length, 'entries');
  console.log('  usage:      ', state.usage);
  console.log('  session age:', Math.round((Date.now() - state.session.createdAt) / 1000), 's');

  fs.writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));
  console.log(`saved to ${STATE_FILE}`);

  // ── Session 2: simulate a restart by reading from disk ──
  console.log('\n--- Session 2: recover from disk ---');
  const saved = JSON.parse(fs.readFileSync(STATE_FILE, 'utf8'));
  const chat2 = new Qwen({
    ...saved.options,
    chatId: saved.chatId,
    lastResponseId: saved.lastResponseId,
    history: saved.history,
    usage: saved.usage,
    session: saved.session,
  });
  console.log('chat2 resumed with:');
  console.log('  chatId:        ', chat2.chatId);
  console.log('  history:       ', chat2.history.length, 'entries');
  console.log('  usage.requests:', chat2.usage.requests);

  // Memory test — should recall facts from session 1
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
