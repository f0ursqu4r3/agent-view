// Viewer smoke test: an in-process demo hub, headless Chromium, and the page's main controls.
// Run with `bun run smoke`. It is not part of `bun test`. Prints `smoke ok` and exits 0 on success.
import { chromium } from 'playwright';
import type { Browser } from 'playwright';
import { Collector } from '../../src/collector/collector';
import { Hub, LocalSource } from '../../src/hub/hub';
import { startServer } from '../../src/hub/server';

const SHOT = process.env.SMOKE_SHOT;

function check(cond: unknown, message: string): asserts cond {
  if (!cond) throw new Error(`smoke failed: ${message}`);
}

const hub = new Hub();
const label = hub.uniqueLabel('smoke');
const local = new LocalSource(new Collector({ host: label, demo: true }), label);
hub.addSource(local);
let server: ReturnType<typeof startServer> | null = null;
let browser: Browser | null = null;
let failure: unknown = null;

try {
  server = startServer({ hub, port: 0 });
  await local.start();

  // SwiftShader gives headless Chromium a software WebGL context on any machine.
  browser = await chromium.launch({ headless: true, args: ['--use-angle=swiftshader', '--use-gl=angle', '--enable-unsafe-swiftshader'] });
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push(`console: ${m.text()}`);
  });
  await page.goto(server.url);
  await page.waitForTimeout(2000);

  check(errors.length === 0, `console errors:\n${errors.join('\n')}`);
  check((await page.locator('#stage canvas').count()) > 0, 'the stage canvas is missing');
  check((await page.locator('.hud').innerText()).includes('SIMULATED'), 'the HUD does not show SIMULATED');
  check(!(await page.locator('#drawer').evaluate((d) => d.classList.contains('open'))), 'the drawer must start closed');

  await page.keyboard.press('t');
  await page.waitForFunction(() => document.getElementById('drawer')?.classList.contains('open') === true, null, { timeout: 2000 });
  // The slide takes 450 ms, but a software-rendered page can be late. Wait until the drawer rests on the bottom edge.
  await page.waitForFunction(() => document.getElementById('drawer')!.getBoundingClientRect().top <= innerHeight * 0.65 + 1, null, { timeout: 5000 });

  await page.keyboard.press('c');
  check(await page.locator('#coverage').isVisible(), '#coverage is not visible after C');
  await page.keyboard.press('c');
  check(!(await page.locator('#coverage').isVisible()), '#coverage is still visible after a second C');

  const canvas = page.locator('#drawer canvas');
  const box = await canvas.boundingBox();
  check(box !== null && box.width > 0 && box.height > 0, 'the timeline canvas has no size');
  // The first lane: 26 px of tick space above it, and a lane is 23 px high.
  await page.mouse.click(box.x + 40, box.y + 26 + 11);
  const focus = await page.evaluate(() => (window as unknown as { __agentView: { store: { focus: string | null } } }).__agentView.store.focus);
  check(typeof focus === 'string' && focus.length > 0, `clicking the first lane did not set the focus (focus: ${String(focus)})`);

  if (SHOT) {
    await page.keyboard.press('c');
    await page.waitForTimeout(500);
    await page.screenshot({ path: SHOT });
  }
  await page.keyboard.press('Escape');
  const cleared = await page.evaluate(() => (window as unknown as { __agentView: { store: { focus: string | null } } }).__agentView.store.focus);
  check(cleared === null, 'Esc did not clear the focus');
  console.log('smoke ok');
} catch (e) {
  failure = e;
} finally {
  await browser?.close().catch(() => {});
  server?.stop();
  await hub.stop().catch(() => {});
}

if (failure) {
  console.error(failure instanceof Error ? failure.message : failure);
  process.exit(1);
}
process.exit(0);
