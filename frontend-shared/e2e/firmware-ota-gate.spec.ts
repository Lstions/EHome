import { test, expect, type Page, type Route } from '@playwright/test'
import { loginAsAdmin } from './helpers/login'

/**
 * E2E-01 … E2E-03：固件闸门的 UI 侧（附录 D / M1b T1b.6）
 *
 * E2E-01 不兼容固件置灰｜E2E-02 force 二次确认需填理由｜E2E-03 有在途任务时删除报错提示
 *
 * ── 运行前提（QA 在隔离栈执行；本地勿起栈）─────────────────────────────────
 * 后端在本机 8080 并同源托管构建产物；账号默认 admin/admin123（可用 E2E_USER /
 * E2E_PASSWORD 覆盖）。用例通过 page.route 注入接口响应，**不写任何业务数据**：
 * 固件列表、建任务 409、删除 409 都是 mock 出来的。
 *
 * 运行：
 *   E2E_BASE=http://127.0.0.1:8080 pnpm exec playwright test e2e/firmware-ota-gate.spec.ts --project=chromium
 */

const BASE = process.env.E2E_BASE || 'http://127.0.0.1:8080'

interface FirmwareRow {
  id: number
  version: string
  filename: string
  checksum: string
  size_bytes: number
  url: string
  created_at: string
  chip: string
  flash_mb: number
  psram: string
  model_key: string
  build_profile: string
  channel: string
  identity_state: string
  withdrawn_at?: string | null
  compatible?: boolean
  reasons?: string[]
}

function firmwareRow(patch: Partial<FirmwareRow> = {}): FirmwareRow {
  return {
    id: 1,
    version: '2.9.0',
    filename: 'ehome_collector-2.9.0.bin',
    checksum: 'a'.repeat(64),
    size_bytes: 1_200_000,
    url: '/api/v1/firmwares/1/download',
    created_at: '2026-11-18T00:00:00Z',
    chip: 'esp32c6',
    flash_mb: 8,
    psram: 'no',
    model_key: 'esp32c6-n8',
    build_profile: 'c6-n8',
    channel: 'stable',
    identity_state: 'verified',
    compatible: true,
    reasons: [],
    ...patch,
  }
}

/** 注入固件列表（契约 §4.3：data 仍是 Firmware[]，不分页）。 */
async function mockFirmwareList(page: Page, rows: FirmwareRow[]): Promise<void> {
  await page.route('**/api/v1/firmwares?**', (route: Route) =>
    route.fulfill({
      status: 200,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ code: 200, message: 'ok', data: rows }),
    })
  )
}

test.describe('固件闸门 UI（E2E-01..03）', () => {
  test.beforeEach(async ({ page }) => {
    // 同 update-banner.spec.ts：登录按钮按可访问名匹配（见 e2e/helpers/login.ts）。
    await loginAsAdmin(page, { baseURL: BASE })
  })

  test('E2E-01 不兼容固件在 OTA 对话框里置灰并显示原因', async ({ page }) => {
    const compatible = firmwareRow({ id: 1, version: '2.9.0' })
    const incompatible = firmwareRow({
      id: 2,
      version: '2.8.0',
      model_key: 'esp32s3-n16',
      build_profile: 's3-n16',
      chip: 'esp32s3',
      flash_mb: 16,
      compatible: false,
      reasons: ['OTA_GATE_FLASH_MISMATCH'],
    })
    await mockFirmwareList(page, [compatible, incompatible])

    await page.goto(BASE + '/node')

    // ⚠️ /node 默认是**卡片视图**（.collector-grid, NodeGridCard），不是表格 ——
    // 旧写法等 .el-table 必然 20s 超时（2026-11-18 D4 实测：.el-table 计数 0）。
    // 等待条件只用业务可见元素：「升级」按钮出现即可（卡片与表格视图都有的动作）。
    const upgradeEntry = page.getByRole('button', { name: '升级', exact: true }).first()
    await expect(upgradeEntry, '节点卡片上的「升级」入口应可见').toBeVisible({ timeout: 20_000 })
    await upgradeEntry.click()

    const dialog = page.locator('.el-dialog', { hasText: 'OTA 固件升级' })
    await expect(dialog).toBeVisible({ timeout: 15_000 })
    await dialog.locator('.el-select').first().click()

    const disabledOption = page.locator('.el-select-dropdown__item.is-disabled', { hasText: '2.8.0' })
    await expect(disabledOption).toBeVisible({ timeout: 10_000 })
    await expect(disabledOption).toContainText('Flash 容量档位与节点不一致')

    // 反证：兼容的那条**不得**被置灰 —— 否则「全置灰」也能让上面两条断言通过
    const enabledOption = page.locator('.el-select-dropdown__item', { hasText: '2.9.0' })
    await expect(enabledOption).toBeVisible()
    await expect(enabledOption).not.toHaveClass(/is-disabled/)
  })

  test('E2E-02 force 二次确认：版本方向可强制，且必须填写理由', async ({ page }) => {
    // 为什么这条列表里必须返回**可选**固件（compatible 缺省/true）：
    // 按「单一模式：命中即置灰」的设计，若列表已标 NOT_AN_UPGRADE，选项被禁用、用户根本
    // 点不进来。force 入口的真实触发路径是「列表未带 node 兼容信息或兼容信息已过期」
    // → 建任务时由闸门 409 拒绝，force 只在这一刻出现（§6.3「命中即 409」）。
    await mockFirmwareList(page, [firmwareRow({ id: 3, version: '1.0.0' })])

    // 建任务返回 409（版本方向 → forceable），并记录每次提交体以便断言 force/理由
    const submitted: Array<Record<string, unknown>> = []
    await page.route('**/api/v1/ota/tasks', (route: Route) => {
      if (route.request().method() !== 'POST') return route.fallback()
      submitted.push(JSON.parse(route.request().postData() || '{}'))
      return route.fulfill({
        status: 409,
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          code: 409,
          message: 'not an upgrade',
          error_code: 'OTA_GATE_NOT_AN_UPGRADE',
          data: { reasons: ['OTA_GATE_NOT_AN_UPGRADE'], forceable: true, fix: '' },
        }),
      })
    })

    await page.goto(BASE + '/node')
    // 同上：默认卡片视图，等待业务元素「升级」即可，不要赌容器类名
    const upgradeEntry = page.getByRole('button', { name: '升级', exact: true }).first()
    await expect(upgradeEntry).toBeVisible({ timeout: 20_000 })
    await upgradeEntry.click()

    const dialog = page.locator('.el-dialog', { hasText: 'OTA 固件升级' })
    await expect(dialog).toBeVisible({ timeout: 15_000 })
    await dialog.locator('.el-select').first().click()
    await page.locator('.el-select-dropdown__item', { hasText: '1.0.0' }).click()
    await dialog.getByTestId('ota-start').click()

    // 409 → 展示原因 + 强制入口；首次提交不应带 force
    await expect(dialog.getByTestId('ota-gate-issue')).toBeVisible({ timeout: 15_000 })
    await expect(dialog.getByTestId('ota-gate-issue')).toContainText('目标版本不高于节点当前版本')
    expect(submitted.at(-1)).toMatchObject({ force: false })

    const forceToggle = dialog.getByTestId('ota-force-toggle')
    await expect(forceToggle).toBeVisible()

    // 未勾选强制 / 未填理由时提交按钮禁用
    await expect(dialog.getByTestId('ota-start')).toBeDisabled()
    await forceToggle.click()
    await expect(dialog.getByTestId('ota-force-reason')).toBeVisible()
    await expect(dialog.getByTestId('ota-start')).toBeDisabled()

    // 填了理由才可提交
    // ⚠️ data-testid 落在 **textarea 本体**上（Element Plus 的 el-input 把 $attrs 透传到内层
    // input/textarea），再 `.locator('textarea')` 会去找「textarea 里的 textarea」⇒ 永远匹配不到、
    // fill 10s 超时（2026-11-18 D5 实测：innerTextareaCount: 0）。直接对 testid 元素 fill。
    await dialog.getByTestId('ota-force-reason').fill('线下已确认可回滚')
    await expect(dialog.getByTestId('ota-start')).toBeEnabled()

    // 强制提交必须把 force 与理由一起送到后端（AC-8：理由进安全审计）
    await dialog.getByTestId('ota-start').click()
    await expect.poll(() => submitted.length, { timeout: 10_000 }).toBeGreaterThanOrEqual(2)
    expect(submitted.at(-1)).toMatchObject({ force: true, force_reason: '线下已确认可回滚' })
  })

  test('E2E-03 有在途任务时删除 → 报错提示说明原因（不是「删除失败」）', async ({ page }) => {
    await mockFirmwareList(page, [firmwareRow({ id: 4, version: '2.7.0' })])
    await page.route('**/api/v1/firmwares/4', (route: Route) => {
      if (route.request().method() !== 'DELETE') return route.fallback()
      return route.fulfill({
        status: 409,
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({
          code: 409,
          message: 'firmware in use',
          error_code: 'FIRMWARE_IN_USE',
          data: { in_flight: 1 },
        }),
      })
    })

    await page.goto(BASE + '/firmware')
    await page.waitForSelector('.el-table', { timeout: 20000 })
    await page.getByRole('button', { name: '删除固件' }).first().click()
    // 确认框（danger 确认，默认焦点在取消）
    await page.locator('.el-message-box__btns button.el-button--danger').click()

    // 提示必须包含「在途 OTA 任务」这一事实
    await expect(page.locator('.el-message', { hasText: '在途 OTA 任务' })).toBeVisible({ timeout: 15000 })
  })
})
