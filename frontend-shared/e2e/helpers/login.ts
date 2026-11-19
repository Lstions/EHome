import { expect, type Locator, type Page } from '@playwright/test'

/**
 * 登录 helper（e2e 公共资产）。
 *
 * ── 为什么必须按**可访问名**找按钮（2026-11-18 D3 实测）────────────────────
 * 登录按钮是 `<el-button type="primary" @click="handleSubmit">`，而 `LoginForm.vue`
 * 没有设 `native-type`，Element Plus 默认渲染成 `type="button"` ⇒
 * `button[type="submit"]` **永远不命中**；
 * 按钮文案是字面量 `'登 录'`（**中间有空格**），`button:has-text("登录")`
 * 也**永远不命中**（`:has-text` 是子串匹配）。
 * 结果是所有 UI 登录的用例都超时在第一步，而不是在业务断言上失败 —— 极难定位。
 * 因此统一用 `getByRole('button', { name: /登\s*录/ })`：按可访问名、容忍空白。
 *
 * ── 输入框为什么用「多选择器兜底」─────────────────────────────────────────
 * Element Plus 的 el-input 会把 placeholder/name/autocomplete 落在**内层 input** 上，
 * 但不同版本/不同写法的落点并不一致（历史上实测过「按 name 找不到」）。
 * 这里按语义强弱组合：autocomplete（浏览器密码管理器语义，最稳定）→ name → placeholder。
 * 任一条命中即可，不会因为某个属性被删就整条用例失效。
 */

/** 登录按钮的可访问名（文案「登 录」，正则容忍中间空白；加载态「登录中...」同样命中）。 */
export const LOGIN_BUTTON_NAME = /登\s*录/

/** 登录相关等待的默认上限：隔离栈冷启动时首屏可能要几秒。 */
export const LOGIN_TIMEOUT_MS = 20_000

/** 登录成功后的落点（默认放行全部一级业务页，避免各 spec 各写一份互相漂移）。 */
export const DEFAULT_POST_LOGIN_URL = /\/(dashboard|node|edge-device|firmware|monitor)/

export interface LoginCredentials {
  username: string
  password: string
}

/**
 * 解析登录凭证：显式入参优先，其次环境变量，最后回落到本地开发默认值。
 * 同时兼容本仓历史上出现过的两套环境变量命名（E2E_* / EHOME_E2E_*）。
 */
export function resolveLoginCredentials(overrides: Partial<LoginCredentials> = {}): LoginCredentials {
  return {
    username:
      overrides.username ||
      process.env.E2E_USER ||
      process.env.EHOME_E2E_USER ||
      process.env.EHOME_E2E_ADMIN_USERNAME ||
      'admin',
    password:
      overrides.password ||
      process.env.E2E_PASSWORD ||
      process.env.EHOME_E2E_PASS ||
      process.env.EHOME_E2E_ADMIN_PASSWORD ||
      'admin123',
  }
}

/** 用户名输入框（多选择器兜底，见文件头）。 */
export function usernameInput(page: Page): Locator {
  return page
    .locator(
      [
        'input[autocomplete="username"]',
        'input[name="username"]',
        'input[placeholder="请输入用户名"]',
      ].join(', '),
    )
    .first()
}

/** 密码输入框（多选择器兜底，见文件头）。 */
export function passwordInput(page: Page): Locator {
  return page
    .locator(['input[autocomplete="current-password"]', 'input[type="password"]'].join(', '))
    .first()
}

/** 登录提交按钮（按可访问名，不依赖 type 与精确文案）。 */
export function loginButton(page: Page): Locator {
  return page.getByRole('button', { name: LOGIN_BUTTON_NAME }).first()
}

/** 填写用户名 + 密码（只填字段，不提交；供需要自定义前后流程的 spec 复用）。 */
export async function fillLoginCredentials(
  page: Page,
  username: string,
  password: string,
): Promise<void> {
  const userField = usernameInput(page)
  await expect(userField, '登录页用户名输入框应可见（Element Plus 已渲染）').toBeVisible({
    timeout: LOGIN_TIMEOUT_MS,
  })
  await userField.fill(username)

  const passField = passwordInput(page)
  await expect(passField, '登录页密码输入框应可见').toBeVisible({ timeout: LOGIN_TIMEOUT_MS })
  await passField.fill(password)
}

/** 点击登录（只提交，不等待跳转；供需要自定义等待条件的 spec 复用）。 */
export async function clickLoginButton(page: Page): Promise<void> {
  const button = loginButton(page)
  await expect(button, '登录按钮应按可访问名「登 录」命中（不要用 type=submit / has-text）').toBeVisible({
    timeout: LOGIN_TIMEOUT_MS,
  })
  await expect(button).toBeEnabled()
  await button.click()
}

export interface LoginOptions extends Partial<LoginCredentials> {
  /** 受测站点地址；省略则用相对路径 '/login'（尊重 playwright.config 的 baseURL）。 */
  baseURL?: string
  /** 登录成功后的 URL 断言（默认放行一级业务页）。 */
  waitForUrl?: RegExp
  /** 等待跳转的超时（默认 25s）。 */
  timeoutMs?: number
}

/**
 * 完整 UI 登录：打开登录页 → 填写 → 提交 → 等待离开登录页。
 * 绝大多数 spec 只需要这一句。
 */
export async function loginAsAdmin(page: Page, options: LoginOptions = {}): Promise<void> {
  const { username, password } = resolveLoginCredentials(options)
  const loginUrl = options.baseURL ? `${options.baseURL.replace(/\/+$/, '')}/login` : '/login'

  await page.goto(loginUrl)
  await fillLoginCredentials(page, username, password)
  await clickLoginButton(page)
  await page.waitForURL(options.waitForUrl ?? DEFAULT_POST_LOGIN_URL, {
    timeout: options.timeoutMs ?? 25_000,
  })
}
