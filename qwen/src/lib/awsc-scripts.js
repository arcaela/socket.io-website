// src/lib/awsc-scripts.js
// Provides the three AWSC script bodies (awsc.js, collina.js, um.js) to the
// jsdom setup. Three sources, tried in order:
//
//   1. An inline `EMBEDDED_AWSC` constant that ships gzipped+base64 versions
//      of all three scripts. This is what `build-bundle.js` injects into
//      `dist/qwen-bundle.js` to produce a fully self-contained single file.
//   2. `<cache-dir>/awsc/*.js` on disk (from a previous run).
//   3. Synchronous download via curl/wget, with a Node subprocess fallback.
//
// The sync download is necessary because the jsdom setup MUST happen at
// require-time of src/index.js (see README for why), and CommonJS does not
// support top-level await.
'use strict';

const fs = require('node:fs');
const path = require('node:path');
const zlib = require('node:zlib');
const { spawnSync } = require('node:child_process');

const { AWSC_DIR, AWSC_URLS, USER_AGENT } = require('./constants');

// Replaced by build-bundle.js at bundling time. Shape:
//   { awsc: '<base64-gzip>', collina: '<base64-gzip>', um: '<base64-gzip>' }
const EMBEDDED_AWSC = null;

function ensureDir(p) { fs.mkdirSync(p, { recursive: true }); }

function readEmbedded() {
  if (!EMBEDDED_AWSC) return null;
  try {
    const out = {};
    for (const [k, b64] of Object.entries(EMBEDDED_AWSC)) {
      out[k] = zlib.gunzipSync(Buffer.from(b64, 'base64')).toString('utf8');
    }
    return out;
  } catch (_) { return null; }
}

function readDiskCache() {
  try {
    const out = {};
    for (const name of ['awsc', 'collina', 'um']) {
      const f = path.join(AWSC_DIR, `${name}.js`);
      if (!fs.existsSync(f) || fs.statSync(f).size < 1000) return null;
      out[name] = fs.readFileSync(f, 'utf8');
    }
    return out;
  } catch (_) { return null; }
}

function downloadSync() {
  ensureDir(AWSC_DIR);
  const proxy =
    process.env.HTTPS_PROXY || process.env.https_proxy ||
    process.env.HTTP_PROXY || process.env.http_proxy || '';

  const curlOK = spawnSync('curl', ['--version'], { encoding: 'utf8' }).status === 0;
  const wgetOK = !curlOK && spawnSync('wget', ['--version'], { encoding: 'utf8' }).status === 0;

  for (const [name, url] of Object.entries(AWSC_URLS)) {
    const file = path.join(AWSC_DIR, `${name}.js`);
    if (fs.existsSync(file) && fs.statSync(file).size > 1000) continue;

    if (curlOK) {
      const args = ['-sS', '-L', '-o', file, url, '--max-time', '30', '-A', USER_AGENT];
      if (proxy) { args.push('--proxy', proxy, '--insecure'); }
      const r = spawnSync('curl', args, { encoding: 'utf8' });
      if (r.status === 0 && fs.existsSync(file) && fs.statSync(file).size > 1000) continue;
    }
    if (wgetOK) {
      const env = { ...process.env };
      if (proxy) env.https_proxy = proxy;
      const r = spawnSync(
        'wget',
        ['-q', '-O', file, url, '--timeout=30', '--no-check-certificate'],
        { encoding: 'utf8', env }
      );
      if (r.status === 0 && fs.existsSync(file) && fs.statSync(file).size > 1000) continue;
    }

    // Last resort: spawn a Node subprocess that uses undici to download.
    // Guarantees the HTTPS_PROXY handling matches our own.
    const code = `
      (async () => {
        const fs = require('node:fs');
        const { request, ProxyAgent } = require('undici');
        const opts = { method: 'GET', headers: { 'User-Agent': ${JSON.stringify(USER_AGENT)} } };
        const p = ${JSON.stringify(proxy)};
        if (p) {
          const u = new URL(p);
          const pa = { uri: u.protocol + '//' + u.host,
                        requestTls: { rejectUnauthorized: false },
                        connect: { rejectUnauthorized: false } };
          if (u.username) pa.token = 'Basic ' + Buffer.from(
              decodeURIComponent(u.username) + ':' + decodeURIComponent(u.password || '')
          ).toString('base64');
          opts.dispatcher = new ProxyAgent(pa);
        }
        const res = await request(${JSON.stringify(url)}, opts);
        if (res.statusCode !== 200) { console.error('HTTP', res.statusCode); process.exit(1); }
        const chunks = [];
        for await (const c of res.body) chunks.push(c);
        fs.writeFileSync(${JSON.stringify(file)}, Buffer.concat(chunks));
      })().catch(e => { console.error(e.message); process.exit(1); });
    `;
    const r = spawnSync(process.execPath, ['-e', code], { encoding: 'utf8', timeout: 30000 });
    if (r.status !== 0) {
      throw new Error(`failed to download ${name} from ${url}: ${(r.stderr || '').slice(0, 200)}`);
    }
  }
  return readDiskCache();
}

// Resolve AWSC scripts from whichever source is available. Throws only if
// none of them work (e.g. first run in an offline container with no bundle).
function getAwscScripts() {
  return readEmbedded() || readDiskCache() || downloadSync();
}

module.exports = { getAwscScripts, readEmbedded, readDiskCache, downloadSync };
