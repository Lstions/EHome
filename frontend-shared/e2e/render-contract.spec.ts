import { test, expect, type Page } from '@playwright/test'

// ─────────────────────────────────────────────────────────────────────────────
// 前端渲染验证：数据源 / 阈值告警 / 自动化规则（字段解析层 selector）
//
// 为什么用 page.route 注入而不连真后端：
//   1. 后端实例受登录限流（实测 429）且审计库不是本批构建；
//   2. 本门禁要验证的是**渲染契约**——"后端给了这些字段，前端是否把它们正确显示出来"，
//      用确定性数据比依赖库里现成行更能钉住：数据形状来自本批后端真实响应（见各 fixture
//      注释的 handler 出处），断言精确到文本。
//
// 数据形状来源：
//   - /api/v1/data-sources        → api/handler_data_source.go（含 category 同域 64 宽）
//   - /api/v1/alert-rules         → api/handler_alert.go
//   - /api/v1/automation-rules    → api/handler_automation.go（trigger_sensor_name 可为 selector）
//   - /api/v1/sensor-fields       → api/handler_sensor_fields.go（本批新增端点）
// ─────────────────────────────────────────────────────────────────────────────

const ENVELOPE = (data: unknown) => JSON.stringify({ code: 0, data, message: 'ok' })

const DATA_SOURCES = {
  items: [
    {
      id: 1, device_id: 7, category: 'rsoc', edge_device_id: 2,
      source_type: 'edge_device', name: 'BMS 主来源', description: '机柜 BMS',
      priority: 100, is_primary: true, max_fail_count: 3, fail_count: 0,
      status: 'active', last_success: '2026-10-22T01:30:00Z', last_failure: null,
    },
    {
      id: 2, device_id: 7, category: 'rsoc', edge_device_id: 3,
      source_type: 'edge_device', name: 'BMS 备来源', description: '',
      priority: 50, is_primary: false, max_fail_count: 3, fail_count: 3,
      status: 'standby', last_success: '2026-10-22T01:29:00Z', last_failure: '2026-10-22T01:00:00Z',
    },
  ],
  total: 2, page: 1, page_size: 20,
}

const ALERT_RULES = [
  {
    id: 11, target_type: 'edge_device', target_id: 2,
    sensor_name: 'temperature_1', comparator: 'gt', threshold: 40,
    duration_sec: 0, silence_sec: 300, level: 'warning', enabled: true,
    resolve_count: 128, match_count: 3, last_matched_at: '2026-10-22T01:31:00Z',
  },
]

const AUTOMATION_RULES = [
  {
    id: 21, name: '日出开关 BMS MOS', enabled: true,
    trigger_type: 'sun_event', trigger_sun_kind: 'sunrise', trigger_sun_offset_min: 0,
    trigger_sensor_name: '', trigger_comparator: '', trigger_threshold: 0,
    action_type: 'device_action', action_device_id: 2, action_id: 'set_mos_policy',
    action_params_json: '{"charge_software_closed":true,"discharge_software_closed":false,"priority":"operator"}',
    require_confirmed: false, cooldown_sec: 300, max_daily_exec: 2,
  },
  {
    id: 22, name: '低电量断开负载', enabled: true,
    trigger_type: 'sensor_threshold', trigger_sensor_name: '@soc',
    trigger_comparator: 'lt', trigger_threshold: 30,
    action_type: 'notification', action_level: 'warning',
    require_confirmed: false, cooldown_sec: 600, max_daily_exec: 0,
  },
]

const SENSOR_FIELDS = {
  edge_device_id: 2,
  device_type: 'jiabaida_bms',
  fields: [
    { name: 'rsoc', unit: '%', label_zh: 'SOC', value_type: 'number', precision: 1,
      sources: ['decl', 'obs'], semantics: ['soc'], samples: 326,
      first_seen: '2026-09-20T08:18:24Z', last_seen: '2026-10-22T01:30:00Z', deprecated: false },
    { name: 'temperature_1', unit: '°C', label_zh: '温度1', value_type: 'number', precision: 1,
      sources: ['obs'], semantics: [], samples: 326,
      first_seen: '2026-09-20T08:18:24Z', last_seen: '2026-10-22T01:30:00Z', deprecated: false },
  ],
  truncated: false,
}

/** 注入已登录会话（token 只是字符串标记；本门禁所有 API 都被 route 拦截）。 */
async function seedSession(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('token', 'uiux-render-token')
    localStorage.setItem('username', 'admin')
  })
}

async function mockAPI(page: Page) {
  await page.route('**/api/v1/**', async (route) => {
    const url = route.request().url()
    const json = (data: unknown) => route.fulfill({ status: 200, contentType: 'application/json', body: ENVELOPE(data) })

    if (url.includes('/api/v1/data-sources/failover-logs')) return json([])
    if (url.includes('/api/v1/data-sources/') && url.includes('/health')) return json([])
    if (url.includes('/api/v1/data-sources')) return json(DATA_SOURCES)
    if (url.includes('/api/v1/alert-rules')) return json(ALERT_RULES)
    if (url.includes('/api/v1/alert-events')) return json({ items: [], total: 0, page: 1, page_size: 20 })
    if (url.includes('/api/v1/automation-rules')) return json(AUTOMATION_RULES)
    if (url.includes('/api/v1/automation-events')) return json({ items: [], total: 0, page: 1, page_size: 20 })
    if (url.includes('/api/v1/sensor-fields')) return json(SENSOR_FIELDS)
    if (url.includes('/api/v1/edge-devices')) return json({ items: [{ id: 2, name: 'BMS-1', type: 'jiabaida_bms' }], total: 1, page: 1, page_size: 20 })
    if (url.includes('/api/v1/notifications')) return json({ items: [], total: 0, page: 1, page_size: 20 })
    if (url.includes('/api/v1/overview')) return json({ node_total: 1, node_online: 1, edge_device_total: 1 })
    return json({ items: [], total: 0, page: 1, page_size: 20 })
  })
}

async function gotoPage(page: Page, path: string, ready: RegExp) {
  await page.goto(path, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.el-table, .el-card, .el-empty', { timeout: 15000 })
  // 等待注入数据真正渲染出来（不是"元素够多"）
  await expect.poll(async () => (await page.locator('body').innerText()).length, { timeout: 15000 }).toBeGreaterThan(50)
  return ready
}

test.describe('前端渲染：数据源 / 告警 / 自动化', () => {
  test.beforeEach(async ({ page }) => {
    await seedSession(page)
    await mockAPI(page)
  })

  test('数据源列表：状态标签 / 类别 / 失败计数逐元素渲染', async ({ page }) => {
    await gotoPage(page, '/data-sources', /数据源/)

    // 表格内两行（主 + 备）
    const rows = page.locator('[data-test="ds-table"] .el-table__row')
    await expect(rows).toHaveCount(2)

    // 状态标签：钉在 data-test="ds-status" 元素上，而不是整页文本。
    // 变异自证要求：把 statusText 的 '权威' 改成别的字，本断言必须变红 ——
    // 整页文本断言做不到（统计卡与按钮里也含"权威"，会掩盖）。
    const statuses = page.locator('[data-test="ds-status"]')
    await expect(statuses.nth(0)).toHaveText('权威')
    await expect(statuses.nth(1)).toHaveText('待命')

    // 类别与名称按行断言
    await expect(rows.nth(0)).toContainText('rsoc')
    await expect(rows.nth(0)).toContainText('BMS 主来源')
    await expect(rows.nth(1)).toContainText('BMS 备来源')

    // 失败计数列以 "0/3" 形式逐行渲染
    await expect(rows.nth(0)).toContainText('0/3')
    await expect(rows.nth(1)).toContainText('3/3')
  })

  test('告警规则：传感器名 / 阈值 / 级别逐格渲染', async ({ page }) => {
    await gotoPage(page, '/alerts', /告警/)

    const table = page.locator('[data-test="rules-table"]')
    await expect(table).toBeVisible()
    const rows = table.locator('.el-table__row')
    await expect(rows).toHaveCount(1)

    // 传感器名与阈值条件在同一行
    await expect(rows.nth(0)).toContainText('temperature_1')
    await expect(rows.nth(0)).toContainText('> 40')
    await expect(rows.nth(0)).toContainText('警告')

    // 空态必须解释原因（渲染契约：没数据不能是空白）
    const body = await page.locator('body').innerText()
    expect(body).toMatch(/暂无告警事件|规则已就绪/)
  })

  test('自动化规则：sun_event 与 selector（@soc）逐行渲染', async ({ page }) => {
    await gotoPage(page, '/automation', /自动化|策略/)

    const table = page.locator('[data-test="rules-table"]')
    await expect(table).toBeVisible()
    const rows = table.locator('.el-table__row')
    await expect(rows).toHaveCount(2)

    // 行 1：日出规则 + 设备动作 id
    await expect(rows.nth(0)).toContainText('日出开关 BMS MOS')
    await expect(rows.nth(0)).toContainText('日出')
    await expect(rows.nth(0)).toContainText('set_mos_policy')

    // 行 2：selector 原样可见 —— @soc 是"规则绑定语义而非物理名"的可见证据
    await expect(rows.nth(1)).toContainText('低电量断开负载')
    await expect(rows.nth(1)).toContainText('@soc')
    await expect(rows.nth(1)).toContainText('通知')
  })

  test('渲染无未捕获异常：三页控制台无 error', async ({ page }) => {
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(String(e)))
    page.on('console', (m) => {
      if (m.type() === 'error') errors.push(m.text())
    })
    for (const p of ['/data-sources', '/alerts', '/automation']) {
      await gotoPage(page, p, /.*/)
    }
    // 过滤网络类错误（route 注入已覆盖 API，不应出现 4xx/5xx）
    // 过滤与本门禁无关的两类噪声：
    //   1. favicon/ResizeObserver（浏览器侧）；
    //   2. WebSocket 连接失败 —— 无后端时必然发生，前端已记 ERROR 日志但页面功能不受影响。
    const real = errors.filter(
      (e) => !/favicon|ResizeObserver|WebSocket|ERR_CONNECTION|Authentication failed/i.test(e),
    )
    expect(real, `控制台错误：\n${real.join('\n')}`).toHaveLength(0)
  })
})
