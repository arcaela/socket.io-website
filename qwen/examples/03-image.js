// examples/03-image.js — image generation.
//
// Returns { url, width, height, model }. The url is a JWT-signed CDN link
// that expires after some hours — download immediately.
//
//   node examples/03-image.js
const fs = require('node:fs');
const path = require('node:path');
const Qwen = require('..');

(async () => {
  // ── static shortcut ──
  console.log('--- Qwen.image (static) ---');
  const r = await Qwen.image(
    'Un gato naranja montando una bicicleta roja en París al atardecer, estilo acuarela.'
  );
  console.log(`model: ${r.model}`);
  console.log(`size:  ${r.width}x${r.height}`);
  console.log(`url:   ${r.url.slice(0, 90)}...`);

  // Download the PNG immediately
  const res = await fetch(r.url);
  const buf = Buffer.from(await res.arrayBuffer());
  const out = path.join(__dirname, 'output.png');
  fs.writeFileSync(out, buf);
  console.log(`saved: ${out} (${buf.length} bytes)`);

  // ── instance, stream mode — observe the keep_alive + image events ──
  console.log('\n--- new Qwen({ stream: true }).image ---');
  await Qwen.warmup({ forceRefresh: true });
  const chat = new Qwen({ stream: true });
  const start = Date.now();
  for await (const ev of chat.image('Un robot pintor en un estudio lleno de cuadros')) {
    if (ev.type === 'info')  console.log(`  ${Date.now() - start}ms  info:`, ev.info.action);
    if (ev.type === 'image') console.log(`  ${Date.now() - start}ms  image:`, ev.width + 'x' + ev.height);
    if (ev.type === 'done')  console.log(`  ${Date.now() - start}ms  done`);
  }
})().catch((e) => {
  if (e instanceof Qwen.QwenRateLimitedError) {
    console.error(`rate limited — retry in ${e.retryAfterHours}h`);
  } else {
    console.error('error:', e.constructor.name, e.message);
  }
  process.exit(1);
});
