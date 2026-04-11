// src/lib/proxy-setup.ts
// Side-effect module: wires $HTTPS_PROXY into undici's global dispatcher so
// that Node's built-in fetch goes out through a corporate / sandbox proxy.
import { ProxyAgent, setGlobalDispatcher } from 'undici';

declare global {
  // eslint-disable-next-line no-var
  var __qwenProxyConfigured: boolean | undefined;
}

(function setupProxy() {
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
    const opts: any = {
      uri: `${u.protocol}//${u.host}`,
      requestTls: { rejectUnauthorized: false },
      connect: { rejectUnauthorized: false },
    };
    if (u.username) {
      const user = decodeURIComponent(u.username);
      const pass = decodeURIComponent(u.password || '');
      opts.token = 'Basic ' + Buffer.from(`${user}:${pass}`).toString('base64');
    }
    setGlobalDispatcher(new ProxyAgent(opts));
  } catch {
    // fall through to direct connections
  }
})();

export {};
