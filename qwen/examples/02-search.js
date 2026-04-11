// examples/02-search.js — web search with structured citations.
//
// `qwen.search()` makes the model call its internal web_search tool. The
// helper extracts the tool_result.docs[] entries into a clean sources[]
// array alongside the normal text reply.
//
//   node examples/02-search.js
const qwen = require('../src');

(async () => {
  const { reply, sources, usage } = await qwen.search(
    'Cuál es el presidente actual de Francia? Respondé en una frase.'
  );

  console.log('reply:', reply);
  console.log('\nsources:');
  for (const s of sources) {
    console.log(`  - ${s.title || '(sin título)'}`);
    console.log(`    ${s.url}`);
    if (s.snippet) console.log(`    ${s.snippet.slice(0, 120)}...`);
    if (s.date) console.log(`    date: ${s.date.trim()}`);
  }
  console.log('\nusage:', usage);
})().catch((e) => {
  console.error('error:', e.message);
  if (e.code) console.error('code:', e.code, 'retryAfterHours:', e.retryAfterHours);
  process.exit(1);
});
