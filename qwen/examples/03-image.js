// examples/03-image.js — image generation.
//
// The Promise form returns { url, width, height, model }. The Stream form
// emits:
//   { type: 'info',  info: { action: 'keep_alive', ... } }   ← during generation
//   { type: 'image', url, width, height }                      ← final image
//   { type: 'done',  image: { url, width, height } }
//
// Note: the URL is a JWT-signed CDN link that expires after some hours.
// Download immediately if you want to keep the bytes.
//
//   node examples/03-image.js
const fs = require('node:fs');
const path = require('node:path');
const qwen = require('../src');

(async () => {
  // ── Promise form ──
  console.log('--- Promise form ---');
  const r = await qwen.image(
    'Un gato naranja montando una bicicleta roja en París al atardecer, estilo acuarela.'
  );
  console.log(`model: ${r.model}`);
  console.log(`size:  ${r.width}x${r.height}`);
  console.log(`url:   ${r.url.slice(0, 90)}...`);

  // Download the PNG immediately so it doesn't disappear when the JWT expires
  const res = await fetch(r.url);
  const buf = Buffer.from(await res.arrayBuffer());
  const out = path.join(__dirname, 'output.png');
  fs.writeFileSync(out, buf);
  console.log(`saved: ${out} (${buf.length} bytes)`);

  // ── Stream form — observe the generation lifecycle ──
  console.log('\n--- Stream form ---');
  await qwen.warmup({ forceRefresh: true });
  const start = Date.now();
  for await (const ev of qwen.image.stream('Un robot pintor en un estudio lleno de cuadros')) {
    if (ev.type === 'info')  console.log(`  ${Date.now() - start}ms  info:`, ev.info.action);
    if (ev.type === 'image') console.log(`  ${Date.now() - start}ms  image:`, ev.width + 'x' + ev.height);
    if (ev.type === 'done')  console.log(`  ${Date.now() - start}ms  done`);
  }
})().catch((e) => {
  if (e instanceof qwen.QwenRateLimitedError) {
    console.error(`rate limited — retry in ${e.retryAfterHours} hours`);
  } else {
    console.error('error:', e.constructor.name, e.message);
  }
  process.exit(1);
});
