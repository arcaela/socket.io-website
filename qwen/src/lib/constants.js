// src/lib/constants.js
// All the hard-coded pieces of the Qwen guest protocol in one place.
// Nothing here depends on the rest of the module so it can be required
// from anywhere (including the bundler) without side effects.
'use strict';

const path = require('node:path');

const BASE = 'https://chat.qwen.ai';

// The frontend's own `/api/v2/configs/` tells us which features are enabled
// for the current session; we just hard-code the endpoint root.
const API = `${BASE}/api/v2`;

// Hard-coded pieces of the frontend contract. Bumping these when the SPA
// upgrades is usually enough to keep things working.
const SPA_VERSION = '0.2.37';
const BX_V = '2.5.36';
const DEFAULT_MODEL = 'qwen3.6-plus';

// Pretend to be a recent desktop Chromium running on Linux. The UA only has
// to be self-consistent with what the AWSC fingerprinter sees — it does not
// have to match the real host.
const USER_AGENT =
  'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) ' +
  'Chrome/147.0.0.0 Safari/537.36';

// Three scripts from Alibaba's CDN that together generate bx-ua / bx-umidtoken
// in the browser. Pinned to specific versions so the token format is stable.
const AWSC_URLS = {
  awsc: 'https://g.alicdn.com/AWSC/AWSC/awsc.js',
  collina: 'https://g.alicdn.com/AWSC/uab/1.140.0/collina.js',
  um: 'https://g.alicdn.com/AWSC/WebUMID/1.93.0/um.js',
};

// Where the AWSC SDK POSTs device-fingerprint data to receive a bx-umidtoken.
// Triggered by um.init() inside our jsdom sandbox.
const UMID_REGISTER_URL = 'https://ynuf.aliapp.org/service/um.json';

// Where we cache AWSC scripts + the active guest session on disk. Overridable
// via QWEN_CACHE_DIR so containers can pin it to a writable volume.
const CACHE_DIR =
  process.env.QWEN_CACHE_DIR ||
  path.join(process.env.HOME || '/tmp', '.cache', 'qwen-node');

const SESSION_FILE = path.join(CACHE_DIR, 'session.json');
const AWSC_DIR = path.join(CACHE_DIR, 'awsc');

// acw_tc (the Alibaba WAF cookie) has a 30-minute TTL. We refresh a bit
// earlier so a long-running process never sees an expired session.
const SESSION_TTL_MS = 25 * 60 * 1000;

module.exports = {
  BASE,
  API,
  SPA_VERSION,
  BX_V,
  DEFAULT_MODEL,
  USER_AGENT,
  AWSC_URLS,
  UMID_REGISTER_URL,
  CACHE_DIR,
  SESSION_FILE,
  AWSC_DIR,
  SESSION_TTL_MS,
};
