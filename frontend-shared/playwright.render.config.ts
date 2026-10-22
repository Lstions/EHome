import { defineConfig, devices } from '@playwright/test'

// 渲染契约门禁（e2e/render-contract.spec.ts）的本地配置。
//
// 与仓库默认 playwright.config.ts 的差异**只有三处**，都是为了让它能在无后端、
// 无 snap chromium 的环境里跑：
//   1. launchOptions.executablePath 由 CHROME_BIN 指定（默认配置写死 /snap/bin/chromium，
//      实测在容器内报 "cannot create transient scope: DBus error"，浏览器根本起不来）；
//   2. webServer 自起 vite preview 托管已构建产物（dist）；
//   3. 只跑 render-contract.spec.ts，不牵连既有需真后端的 spec。
const PORT = Number(process.env.RENDER_PORT || 4173)

export default defineConfig({
  testDir: './e2e',
  testMatch: /(render-contract|dump)\.spec\.ts/,
  timeout: 45 * 1000,
  fullyParallel: false,
  reporter: [['list']],
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    headless: true,
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
    launchOptions: process.env.CHROME_BIN
      ? { executablePath: process.env.CHROME_BIN, args: ['--no-sandbox', '--disable-setuid-sandbox', '--disable-dev-shm-usage'] }
      : { args: ['--no-sandbox', '--disable-setuid-sandbox'] },
  },
  webServer: {
    command: `npx vite preview --port ${PORT} --strictPort`,
    port: PORT,
    reuseExistingServer: true,
    timeout: 60 * 1000,
  },
  projects: [{ name: 'render-contract', use: { ...devices['Desktop Chrome'] } }],
})
