// src/lib/tokens.ts
// Drives AWSC's um.init() (which POSTs to ynuf.aliapp.org) and reads the
// resulting bx-umidtoken, plus the synchronous bx-ua from uab.getUA().
import { UMID_REGISTER_URL } from './constants';
import type { JsdomEnv } from './jsdom-env';

export interface TokenPair {
  bxUa: string;
  umidToken: string;
}

export async function acquireTokens(env: JsdomEnv): Promise<TokenPair> {
  const { window, xhrLog } = env;

  // Clear stale entries from previous invocations in the same process
  xhrLog.length = 0;

  // Event-loop yield + AWSC internal init wait (see README § ordering quirk)
  await new Promise<void>((r) => setImmediate(r));
  await new Promise<void>((r) => setTimeout(r, 1500));

  // 1) bx-umidtoken — triggered by um.init()
  let umMod: any = null;
  window.AWSC.use('um', (_: any, mod: any) => { umMod = mod; });
  if (!umMod || typeof umMod.init !== 'function') {
    throw new Error('AWSC umid module not available after setup');
  }
  try {
    umMod.init({
      appName: 'baxia',
      appKey: 'chat.qwen.ai',
      getToken: (cb: any) => { if (typeof cb === 'function') cb('test-token'); },
    });
  } catch (e: any) {
    throw new Error(`um.init threw: ${e.message}`);
  }

  // Poll xhrLog for the umid registration response
  let umidToken: string | null = null;
  const deadline = Date.now() + 12000;
  while (!umidToken && Date.now() < deadline) {
    await new Promise<void>((r) => setTimeout(r, 150));
    for (const entry of xhrLog) {
      if (!entry.responseText || !entry.url) continue;
      if (!entry.url.includes('ynuf.aliapp.org')) continue;
      try {
        const j = JSON.parse(entry.responseText);
        if (j && j.tn) { umidToken = j.tn; break; }
      } catch {}
    }
  }
  if (!umidToken) {
    throw new Error(
      `umid registration did not return a bx-umidtoken ` +
      `(is ${UMID_REGISTER_URL} reachable from this pod?)`
    );
  }

  // 2) bx-ua — synchronous
  let uabMod: any = null;
  window.AWSC.use('uab', (_: any, mod: any) => { uabMod = mod; });
  if (!uabMod || typeof uabMod.getUA !== 'function') {
    throw new Error('AWSC uab module not available');
  }
  const bxUa = uabMod.getUA();
  if (typeof bxUa !== 'string' || bxUa.length < 100) {
    throw new Error(`unexpected bx-ua format (length ${bxUa && bxUa.length}): ${bxUa}`);
  }

  return { bxUa, umidToken };
}
