// examples/02-search.js — web search with structured citations.
//
// `search` activates Qwen's built-in web_search tool. The Promise form
// returns the accumulated reply + deduped sources. The Stream form emits:
//   { type: 'tool_call', name: 'web_search', arguments: '<streamed JSON>' }
//   { type: 'sources',   sources: [{url,title,snippet,date,hostname}] }
//   { type: 'text',      content, fullContent }
//   { type: 'done',      reply, sources, usage }
//
//   node examples/02-search.js
const qwen = require('../src');

(async () => {
  // ── Promise form ──
  console.log('--- Promise form ---');
  const { reply, sources, usage } = await qwen.search(
    'Dame 3 noticias de tecnología de los últimos días con fuentes reales'
  );
  console.log('reply:', reply.slice(0, 200) + '...');
  console.log(`\nsources (${sources.length}):`);
  for (const s of sources.slice(0, 5)) {
    console.log(`  - ${(s.title || '(sin título)').slice(0, 80)}`);
    console.log(`    ${s.url}`);
  }
  console.log('\nusage:', usage && usage.total_tokens, 'tokens');

  // ── Stream form ──
  console.log('\n--- Stream form ---');
  await qwen.warmup({ forceRefresh: true });  // new session to avoid quota bleed
  for await (const ev of qwen.search.stream('¿Quién ganó el Oscar 2026 a mejor película?')) {
    if (ev.type === 'tool_call') {
      process.stdout.write('.');                  // searching...
    } else if (ev.type === 'sources') {
      console.log(`\nreceived ${ev.sources.length} sources`);
    } else if (ev.type === 'text') {
      process.stdout.write(ev.content);
    } else if (ev.type === 'done') {
      console.log(`\n\n[done] ${ev.sources.length} sources total`);
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
