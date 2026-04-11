// src/lib/cookies.js
// Fetches a fresh Cookie header by GET-ing chat.qwen.ai/. Only two of the
// cookies the server sets actually matter for backend auth:
//
//   acw_tc   — Alibaba Cloud WAF session cookie (30 min TTL)
//   x-ap     — POP routing cookie
//
// The rest (tfstk, isg, cna, atpsida, ssxmod_itna, …) are tracker cookies
// that are NOT validated by the /api/v2/chat/completions endpoint — we
// empirically verified this during reverse-engineering. We still pass
// everything the server sent us because (a) it costs nothing and (b) it
// makes us look more like a real browser.
'use strict';

const { BASE, USER_AGENT } = require('./constants');

async function fetchFreshCookies() {
  const res = await fetch(BASE + '/', {
    method: 'GET',
    headers: { 'User-Agent': USER_AGENT, Accept: 'text/html' },
  });
  const setCookies = typeof res.headers.getSetCookie === 'function'
    ? res.headers.getSetCookie()
    : [res.headers.get('set-cookie')].filter(Boolean);
  return setCookies
    .map((c) => c.split(';')[0].trim())
    .filter(Boolean)
    .join('; ');
}

module.exports = { fetchFreshCookies };
