// src/lib/jsdom-env.js
// Builds a JSDOM window pre-loaded with AWSC's anti-bot scripts.
// Returns { window, xhrLog, dom }.
//
// ⚠️ CRITICAL ORDERING CONSTRAINT
// This function MUST be invoked from synchronous module-level code (i.e.
// during a `require()` call), NOT from inside an async function. The AWSC
// scripts schedule internal setTimeout chains that only fire correctly
// when the eval happens before any `await` statement on the current call
// stack. If you call createJsdomEnv() from an async function's body you'll
// silently get a window where `AWSC.use('um').init()` never triggers its
// XHR, and subsequently there's no way to obtain a bx-umidtoken.
//
// See the README under "ordering quirk" for the full story.
'use strict';

const { JSDOM, VirtualConsole } = require('jsdom');
const { BASE, USER_AGENT } = require('./constants');
const { createNodeXHR } = require('./node-xhr');

function createJsdomEnv(awscScripts) {
  const vc = new VirtualConsole();
  vc.on('jsdomError', (e) => {
    // jsdom doesn't ship Canvas or WebGL backends. AWSC's fingerprinter
    // probes them and gets warnings; we silence those because the tokens
    // it produces are still accepted by Qwen's backend.
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

  // Install the Node-backed XMLHttpRequest polyfill so AWSC's `um.js` can
  // POST to https://ynuf.aliapp.org/service/um.json through our fetch stack.
  const { NodeXHR, xhrLog } = createNodeXHR();
  window.XMLHttpRequest = NodeXHR;

  // Swallow telemetry Image pings (some of the AWSC scripts set `new Image().src = beaconUrl`
  // for fire-and-forget metrics; letting those actually fire would leak hostnames
  // and potentially rate-limit us on internal APM endpoints).
  const _realImage = window.Image; // eslint-disable-line no-unused-vars
  function StubImage() {
    const img = { _src: null };
    Object.defineProperty(img, 'src', {
      get() { return img._src; },
      set(v) { img._src = v; },
    });
    return img;
  }
  window.Image = StubImage;

  // Evaluate AWSC bundles in order. The loader (awsc.js) must run first so
  // that collina.js and um.js can register themselves against `window.AWSC`.
  for (const name of ['awsc', 'collina', 'um']) {
    try { window.eval(awscScripts[name]); }
    catch (e) { throw new Error(`failed to load ${name}: ${e.message}`); }
  }

  return { window, xhrLog, dom };
}

module.exports = { createJsdomEnv };
