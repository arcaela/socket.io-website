// examples/02-search.js — web search with structured citations.
//
//   node examples/02-search.js
const Qwen = require('..');

(async () => {
  // ── static shortcut ──
  console.log('--- Qwen.search (static) ---');
  const { reply, sources, usage } = await Qwen.search(
    'Dame 3 noticias de tecnología de los últimos días con fuentes'
  );
  console.log('reply:', reply.slice(0, 200) + '...');
  console.log(`\nsources (${sources.length}):`);
  for (const s of sources.slice(0, 5)) {
    console.log(`  - ${(s.title || '(sin título)').slice(0, 80)}`);
    console.log(`    ${s.url}`);
  }
  console.log('\nusage:', usage && usage.total_tokens, 'tokens');

  // ── instance, stream mode — watch tool_call / sources / text events live ──
  console.log('\n--- new Qwen({ stream: true }).search ---');
  await Qwen.warmup({ forceRefresh: true });  // avoid quota bleed
  const chat = new Qwen({ stream: true });
  for await (const ev of chat.search('¿Quién ganó el Oscar 2026 a mejor película?')) {
    if (ev.type === 'tool_call') process.stdout.write('.');   // searching...
    else if (ev.type === 'sources') console.log(`\nreceived ${ev.sources.length} sources`);
    else if (ev.type === 'text') process.stdout.write(ev.content);
    else if (ev.type === 'done') console.log(`\n\n[done] ${(ev.sources || []).length} sources`);
  }
})().catch((e) => {
  if (e instanceof Qwen.QwenRateLimitedError) {
    console.error(`rate limited — retry in ${e.retryAfterHours}h`);
  } else {
    console.error('error:', e.constructor.name, e.message);
  }
  process.exit(1);
});
