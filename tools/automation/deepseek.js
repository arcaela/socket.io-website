// Playwright script to navigate to DeepSeek using the Xvfb virtual display.
// Run with:  DISPLAY=:99 node deepseek.js
const { chromium } = require('playwright');
const path = require('path');

function parseProxyFromEnv() {
  const raw = process.env.HTTPS_PROXY || process.env.https_proxy || process.env.HTTP_PROXY || process.env.http_proxy;
  if (!raw) return null;
  try {
    const u = new URL(raw);
    const proxy = { server: `${u.protocol}//${u.host}` };
    if (u.username) proxy.username = decodeURIComponent(u.username);
    if (u.password) proxy.password = decodeURIComponent(u.password);
    return proxy;
  } catch (e) {
    console.warn('[deepseek] could not parse proxy env var:', e.message);
    return null;
  }
}

(async () => {
  const headless = process.env.HEADLESS === '1';
  const proxy = parseProxyFromEnv();
  console.log(
    `[deepseek] launching chromium (headless=${headless}, DISPLAY=${process.env.DISPLAY || 'unset'}, proxy=${proxy ? proxy.server : 'none'})`
  );

  const browser = await chromium.launch({
    headless,
    proxy: proxy || undefined,
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  });

  const context = await browser.newContext({
    viewport: { width: 1280, height: 720 },
    ignoreHTTPSErrors: true,
    userAgent:
      'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36',
  });
  const page = await context.newPage();

  const url = process.argv[2] || 'https://www.deepseek.com/';
  console.log(`[deepseek] navigating to ${url}`);
  const response = await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 60000 });
  console.log(`[deepseek] HTTP ${response && response.status()}`);

  // Give the page a moment to render client-side content
  await page.waitForLoadState('networkidle', { timeout: 30000 }).catch(() => {});
  const title = await page.title();
  console.log(`[deepseek] title: ${title}`);

  const shotPath = path.join(__dirname, 'deepseek.png');
  await page.screenshot({ path: shotPath, fullPage: true });
  console.log(`[deepseek] screenshot saved to ${shotPath}`);

  await browser.close();
  console.log('[deepseek] done');
})().catch((err) => {
  console.error('[deepseek] error:', err);
  process.exit(1);
});
