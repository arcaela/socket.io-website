// examples/03-image.js — image generation.
//
// `qwen.image()` returns { url, width, height, model }. The url is a
// JWT-signed CDN link to a PNG hosted on cdn.qwenlm.ai. It expires after
// some hours — download it immediately if you need to keep the asset.
//
//   node examples/03-image.js
const fs = require('node:fs');
const path = require('node:path');
const qwen = require('../src');

(async () => {
  const result = await qwen.image(
    'Un gato naranja montando una bicicleta roja en París al atardecer, estilo acuarela.'
  );

  console.log('model: ', result.model);
  console.log('size:  ', `${result.width}x${result.height}`);
  console.log('url:   ', result.url);

  // Download the PNG so it doesn't disappear when the JWT expires.
  const res = await fetch(result.url);
  const buf = Buffer.from(await res.arrayBuffer());
  const out = path.join(__dirname, 'output.png');
  fs.writeFileSync(out, buf);
  console.log(`saved: ${out} (${buf.length} bytes)`);
})().catch((e) => {
  console.error('error:', e.message);
  if (e.code) console.error('code:', e.code);
  process.exit(1);
});
