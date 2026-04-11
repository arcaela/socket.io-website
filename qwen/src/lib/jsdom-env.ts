// src/lib/jsdom-env.ts
// Builds a jsdom window pre-loaded with AWSC's anti-bot scripts.
//
// ⚠️ ORDERING CONSTRAINT: this must be invoked from synchronous module-top
// code, NOT from inside an async function — otherwise the AWSC scripts'
// internal setTimeout chains never fire.
import { JSDOM, VirtualConsole } from 'jsdom';
import { BASE, USER_AGENT } from './constants';
import { createNodeXHR, XhrLogEntry } from './node-xhr';
import type { AwscScripts } from './awsc-scripts';

export interface JsdomEnv {
  window: any;
  xhrLog: XhrLogEntry[];
  dom: JSDOM;
}

export function createJsdomEnv(awscScripts: AwscScripts): JsdomEnv {
  const vc = new VirtualConsole();
  vc.on('jsdomError', (e: Error) => {
    if (/HTMLCanvasElement's getContext/.test(e.message)) return;
    if (process.env.QWEN_DEBUG) console.error('[jsdom]', e.message);
  });

  const dom = new JSDOM('<!doctype html><html><body></body></html>', {
    url: BASE + '/',
    runScripts: 'outside-only',
    pretendToBeVisual: true,
    virtualConsole: vc,
  });
  const { window } = dom;

  Object.defineProperty(window.navigator, 'userAgent', {
    value: USER_AGENT,
    configurable: true,
  });

  const { NodeXHR, xhrLog } = createNodeXHR();
  (window as any).XMLHttpRequest = NodeXHR;

  function StubImage(this: any) {
    const img: any = { _src: null };
    Object.defineProperty(img, 'src', {
      get() { return img._src; },
      set(v: string) { img._src = v; },
    });
    return img;
  }
  (window as any).Image = StubImage;

  for (const name of ['awsc', 'collina', 'um'] as const) {
    try {
      (window as any).eval(awscScripts[name]);
    } catch (e: any) {
      throw new Error(`failed to load ${name}: ${e.message}`);
    }
  }

  return { window, xhrLog, dom };
}
