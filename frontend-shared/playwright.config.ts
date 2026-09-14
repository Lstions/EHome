import { defineConfig, devices } from '@playwright/test';

// UI/UX 回归门禁（e2e/uiux-regression.spec.ts）的受测地址。
// 前端静态产物由后端同源托管（审计环境约定，见 docs/分析/UIUX审计契约-2026-09-13.md §3）。
// 默认指向审计实例 8082；自建实例时用 UIUX_BASE 覆盖，无需改本文件。
const UIUX_BASE = process.env.UIUX_BASE || 'http://127.0.0.1:8082';

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

    // ── UI/UX 回归门禁矩阵（本任务新增，2026-09-13）──────────────────────────
    // 为什么需要 project 而不只是在 spec 里 setViewportSize：
    //   `setViewportSize` 只能改尺寸，**改不了输入方式**。`(pointer: coarse)` 是
    //   独立媒体查询（规范 §4.4.7 MUST），只有 `hasTouch`/`isMobile` 的浏览器上下文
    //   才会命中 —— theme.css:644-660 的粗指针分支就是靠它生效的。
    //   故必须用一个真实粗指针 project 跑一遍，否则该分支被删除时门禁不会响。
    //
    // 用 testMatch 限定只吃本门禁文件：避免把既有 spec（auth/device/system-flow）
    // 也乘以矩阵倍数。**既有的 chromium project 未做任何改动。**
    {
      name: 'uiux-gate-fine',
      testMatch: /uiux-regression\.spec\.ts/,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: UIUX_BASE,
        viewport: { width: 1440, height: 900 },
        colorScheme: 'light',
        hasTouch: false,
      },
    },
    {
      name: 'uiux-gate-coarse',
      testMatch: /uiux-regression\.spec\.ts/,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: UIUX_BASE,
        viewport: { width: 390, height: 844 },
        colorScheme: 'dark',
        hasTouch: true,
        isMobile: true,
        deviceScaleFactor: 2,
      },
    },
  ],
});
