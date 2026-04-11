// src/lib/proxy-setup.js
// Side-effect module: wires $HTTPS_PROXY / $HTTP_PROXY into undici's global
// dispatcher so that Node's built-in fetch (and our NodeXHR polyfill) goes
// out through a corporate / sandbox proxy. Without this, Node fetch ignores
// the env vars entirely.
//
// Required from src/index.js BEFORE any fetch happens.
'use strict';

const { ProxyAgent, setGlobalDispatcher } = require('undici');

(function setupProxy() {
  // Guard: only do this once per process, even if the module is required
  // multiple times through different paths (e.g. bundle + unbundled copy).
  if (globalThis.__qwenProxyConfigured) return;
  globalThis.__qwenProxyConfigured = true;

  const raw =
    process.env.HTTPS_PROXY ||
    process.env.https_proxy ||
    process.env.HTTP_PROXY ||
    process.env.http_proxy;
  if (!raw) return;

  try {
    const u = new URL(raw);
    const opts = {
      uri: `${u.protocol}//${u.host}`,
      // Many egress proxies terminate TLS with their own internal CA; we
      // don't pin that CA so we skip verification on the tunnel. The target
      // (chat.qwen.ai) is still verified by the proxy itself.
      requestTls: { rejectUnauthorized: false },
      connect: { rejectUnauthorized: false },
    };
    if (u.username) {
      const user = decodeURIComponent(u.username);
      const pass = decodeURIComponent(u.password || '');
      opts.token = 'Basic ' + Buffer.from(`${user}:${pass}`).toString('base64');
    }
    setGlobalDispatcher(new ProxyAgent(opts));
  } catch (_) {
    // Malformed proxy URL: fall through to direct connections.
  }
})();
