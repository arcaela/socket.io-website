// src/lib/cookies.ts
// GET https://chat.qwen.ai/ and harvest the Set-Cookie headers into a
// single Cookie string. Only acw_tc and x-ap actually matter for backend
// auth, but we pass everything through to look like a real browser.
import { BASE, USER_AGENT } from './constants';

export async function fetchFreshCookies(): Promise<string> {
  const res = await fetch(BASE + '/', {
    method: 'GET',
    headers: { 'User-Agent': USER_AGENT, Accept: 'text/html' },
  });
  const h = res.headers as any;
  const setCookies: string[] = typeof h.getSetCookie === 'function'
    ? h.getSetCookie()
    : [res.headers.get('set-cookie')].filter(Boolean) as string[];
  return setCookies
    .map((c) => c.split(';')[0].trim())
    .filter(Boolean)
    .join('; ');
}
