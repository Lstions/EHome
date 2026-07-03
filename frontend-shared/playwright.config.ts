import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  testMatch: /.*\.spec\.ts$/,
  testIgnore: /_archive\/.*/,
  timeout: 30 * 1000,
  fullyParallel: false,
  reporter: [
    ['list'],
    ['json', { outputFile: '/tmp/e2e-shots/report.json' }],
    ['html', { outputFolder: '/tmp/e2e-shots/html-report' }]
  ],
  use: {
    baseURL: 'http://localhost:5174',
    headless: true,
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
    screenshot: 'only-on-failure',
    video: undefined,
    trace: 'retain-on-failure',
    actionTimeout: 10 * 1000,
    // Use system chromium
    launchOptions: {
      executablePath: '/snap/bin/chromium',
      args: ['--no-sandbox', '--disable-setuid-sandbox']
    }
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
});
