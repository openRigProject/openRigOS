import { test, expect } from '@playwright/test';

test.describe('Hotspot management page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/hotspot');
  });

  // ── Basic load ─────────────────────────────────────────────────────────

  test('loads without redirecting to provisioning wizard', async ({ page }) => {
    await expect(page).toHaveURL('/hotspot');
    // Tab bar is always visible regardless of active tab.
    await expect(page.getByRole('button', { name: 'Hotspot' })).toBeVisible();
  });

  // ── WASM initialisation ────────────────────────────────────────────────

  test('WASM client sets window.openrig', async ({ page }) => {
    // Go's goroutine scheduler is async — window.openrig is set after
    // go.run() yields, so we poll rather than check immediately.
    await expect.poll(
      () => page.evaluate(() => typeof (window as any).openrig),
      { timeout: 10_000 },
    ).toBe('object');
  });

  // ── Hotspot tab — DMR / YSF ────────────────────────────────────────────
  //
  // The Hotspot panel is hidden until its tab is clicked; elements inside
  // a hidden panel are not "visible" in Playwright's sense, so we click
  // the tab in a nested beforeEach that runs after the outer one.

  test.describe('Hotspot tab', () => {
    test.beforeEach(async ({ page }) => {
      await page.getByRole('button', { name: 'Hotspot' }).click();
    });

    test('DMR server select is populated after page load', async ({ page }) => {
      await expect.poll(
        () => page.locator('#dmr-server option').count(),
        { timeout: 15_000, intervals: [500] },
      ).toBeGreaterThan(1);
    });

    test('selected DMR server value is a non-empty string', async ({ page }) => {
      await expect.poll(
        () => page.locator('#dmr-server option').count(),
        { timeout: 15_000 },
      ).toBeGreaterThan(1);

      const value = await page.locator('#dmr-server').inputValue();
      expect(value.length).toBeGreaterThan(0);
    });

    test('changing DMR network repopulates server select with different servers', async ({ page }) => {
      // Wait for the initial BrandMeister list (many servers, so count > 1).
      await expect.poll(
        () => page.locator('#dmr-server option').count(),
        { timeout: 15_000 },
      ).toBeGreaterThan(1);

      const bmFirst = await page.locator('#dmr-server option').first().getAttribute('value');

      // Switch to TGIF — triggers onDmrNetworkChange → loadDmrServerList.
      await page.selectOption('#dmr-network', 'tgif');

      // Wait until the "Loading..." placeholder (value="") is replaced.
      // TGIF has only one server, so we check for a non-empty value rather than count > 1.
      await expect.poll(
        () => page.locator('#dmr-server option').first().getAttribute('value'),
        { timeout: 15_000 },
      ).not.toBe('');

      const tgifFirst = await page.locator('#dmr-server option').first().getAttribute('value');
      expect(tgifFirst).not.toBe(bmFirst);
    });

    test('YSF reflector select is populated after page load', async ({ page }) => {
      await expect.poll(
        () => page.locator('#ysf-reflector option').count(),
        { timeout: 15_000, intervals: [500] },
      ).toBeGreaterThan(1);
    });

    test('switching to FCS shows and populates FCS room select', async ({ page }) => {
      await page.selectOption('#ysf-network', 'fcs');

      await expect(page.locator('#ysf-fcs-group')).toBeVisible();

      await expect.poll(
        () => page.locator('#fcs-room option').count(),
        { timeout: 15_000 },
      ).toBeGreaterThan(1);
    });

    test('switching to Custom shows text input, not a select', async ({ page }) => {
      await page.selectOption('#ysf-network', 'custom');

      await expect(page.locator('#ysf-custom-group')).toBeVisible();
      await expect(page.locator('#ysf-custom-server')).toBeVisible();
      // Reflector and FCS groups should be hidden.
      await expect(page.locator('#ysf-reflector-group')).toBeHidden();
      await expect(page.locator('#ysf-fcs-group')).toBeHidden();
    });
  });
});
