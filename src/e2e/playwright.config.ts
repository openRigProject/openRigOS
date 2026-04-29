import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  retries: process.env.CI ? 2 : 0,
  use: {
    baseURL: 'http://localhost:8080',
    trace: 'on-first-retry',
  },
  globalSetup: './global-setup',
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
  ],
  // Run `bash ../dev.sh` from src/e2e/ — dev.sh cds into src/ itself.
  // reuseExistingServer lets you keep dev.sh running between test runs locally.
  webServer: {
    command: 'bash ../dev.sh',
    url: 'http://localhost:8080',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000, // Go + WASM builds can take a minute on first run
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
