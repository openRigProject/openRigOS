import { test, expect } from '@playwright/test';

test.describe('Hotspot management page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/hotspot');
  });

  // ── Basic load ─────────────────────────────────────────────────────────

  test('loads without redirecting to provisioning wizard', async ({ page }) => {
    await expect(page).toHaveURL('/hotspot');
    await expect(page.getByRole('button', { name: 'Hotspot' })).toBeVisible();
  });

  // ── WASM initialisation ────────────────────────────────────────────────

  test('WASM client sets window.openrig', async ({ page }) => {
    await expect.poll(
      () => page.evaluate(() => typeof (window as any).openrig),
      { timeout: 10_000 },
    ).toBe('object');
  });

  // ── Hotspot tab — DMR / YSF ────────────────────────────────────────────

  test.describe('Hotspot tab', () => {
    test.beforeEach(async ({ page }) => {
      await page.getByRole('button', { name: 'Hotspot' }).click();
    });

    test('DMR server combo is populated after page load', async ({ page }) => {
      // The hidden input gets a value once the server list loads.
      await expect.poll(
        () => page.locator('#dmr-server').inputValue(),
        { timeout: 15_000, intervals: [500] },
      ).not.toBe('');
    });

    test('DMR server search input reflects the selected value', async ({ page }) => {
      await expect.poll(
        () => page.locator('#dmr-server-input').inputValue(),
        { timeout: 15_000 },
      ).not.toBe('');
    });

    test('DMR server dropdown filters on search input', async ({ page }) => {
      // Wait for list to load.
      await expect.poll(
        () => page.locator('#dmr-server').inputValue(),
        { timeout: 15_000 },
      ).not.toBe('');

      await page.locator('#dmr-server-input').click();
      await page.locator('#dmr-server-input').fill('us');

      // Dropdown should be open and show only matching options.
      await expect(page.locator('#dmr-server-dropdown')).toHaveClass(/open/);
      const options = page.locator('#dmr-server-dropdown .combo-option');
      await expect(options.first()).toBeVisible();
      const text = await options.first().textContent();
      expect(text?.toLowerCase()).toContain('us');
    });

    test('changing DMR network repopulates server combo with different servers', async ({ page }) => {
      // Wait for BrandMeister value.
      await expect.poll(
        () => page.locator('#dmr-server').inputValue(),
        { timeout: 15_000 },
      ).not.toBe('');

      const bmValue = await page.locator('#dmr-server').inputValue();

      await page.selectOption('#dmr-network', 'tgif');

      // Wait for the TGIF value to replace the BrandMeister value.
      await expect.poll(
        () => page.locator('#dmr-server').inputValue(),
        { timeout: 15_000 },
      ).not.toBe('');

      const tgifValue = await page.locator('#dmr-server').inputValue();
      expect(tgifValue).not.toBe(bmValue);
    });

    test('YSF reflector combo is populated after page load', async ({ page }) => {
      await expect.poll(
        () => page.locator('#ysf-reflector').inputValue(),
        { timeout: 15_000, intervals: [500] },
      ).not.toBe('');
    });

    test('switching to FCS shows and populates FCS room combo', async ({ page }) => {
      await page.selectOption('#ysf-network', 'fcs');

      await expect(page.locator('#ysf-fcs-group')).toBeVisible();

      await expect.poll(
        () => page.locator('#fcs-room').inputValue(),
        { timeout: 15_000 },
      ).not.toBe('');
    });

    test('switching to Custom shows text input, not a combo', async ({ page }) => {
      await page.selectOption('#ysf-network', 'custom');

      await expect(page.locator('#ysf-custom-group')).toBeVisible();
      await expect(page.locator('#ysf-custom-server')).toBeVisible();
      await expect(page.locator('#ysf-reflector-group')).toBeHidden();
      await expect(page.locator('#ysf-fcs-group')).toBeHidden();
    });
  });
});
