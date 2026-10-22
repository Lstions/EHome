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
    // 来源组规则（方案 v0.3）：trigger_edge_device_id 是后端双写的锚点，前端摘要必须标注来源组
    source_kind: 'source_group', source_group_id: 7, trigger_edge_device_id: 2,
    action_type: 'notification', action_level: 'warning',
    require_confirmed: false, cooldown_sec: 600, max_daily_exec: 0,
  },
]

/** 主备组：active_member_id=11（后端必须返回；复合组为 0/null，见契约裁决）。 */
const PRIMARY_GROUP = {
  id: 7, name: '客厅光照主备', kind: 'primary_backup', category: 'illumination',
  combine: '', min_members: 0, staleness_sec: 0, on_degrade: '',
  enabled: true, revision: 7, description: '', active_member_id: 11,
  members: [
    { id: 11, group_id: 7, edge_device_id: 2, priority: 100, required: false, status: 'active', fail_count: 0, max_fail_count: 3, last_success: '2026-10-22T05:00:00Z', last_failure: null },
    { id: 12, group_id: 7, edge_device_id: 3, priority: 50, required: false, status: 'standby', fail_count: 0, max_fail_count: 3, last_success: null, last_failure: null },
  ],
}

/** 复合组：combine=avg / min_members=1；**没有 active 概念**（active_member_id=0）。 */
const COMPOSITE_GROUP = {
  id: 8, name: '1+2 光照合成', kind: 'composite', category: 'illumination',
  combine: 'avg', min_members: 1, staleness_sec: 30, on_degrade: 'fail_closed',
  enabled: true, revision: 3, description: '', active_member_id: 0,
  members: [
    { id: 21, group_id: 8, edge_device_id: 2, priority: 100, required: true, status: '', fail_count: 0, max_fail_count: 3, last_success: null, last_failure: null },
    { id: 22, group_id: 8, edge_device_id: 3, priority: 50, required: true, status: '', fail_count: 0, max_fail_count: 3, last_success: null, last_failure: null },
  ],
}

const SOURCE_GROUPS = { items: [PRIMARY_GROUP, COMPOSITE_GROUP], total: 2, page: 1, page_size: 20 }

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

    if (/\/api\/v1\/source-groups\/\d+\/(health|failover-logs)/.test(url)) return json([])
    if (url.includes('/api/v1/source-groups')) return json(SOURCE_GROUPS)
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

/** 选择 Element Plus 下拉项（按可见文本；只用于本门禁新增的来源选择器）。 */
async function pickOption(page: Page, selectTestId: string, optionText: string | RegExp) {
  await page.locator(`[data-test="${selectTestId}"]`).click()
  const item = page.locator('.el-select-dropdown__item', { hasText: optionText }).first()
  await expect(item).toBeVisible({ timeout: 5000 })
  await item.click()
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


// ─────────────────────────────────────────────────────────────────────────────
// 来源组（规则数据来源，方案 v0.3 / 契约 M0）—— 与上面同一门禁、同一注入范式。
//
// 为什么单独一个 describe 而不是塞进上一组：这批断言的"分母"是新增的两个对象
// （source_group / source_group_member），失败时能直接定位是哪一批契约漂移。
// 覆盖：列表逐元素、**composite 无激活按钮的负例（SG-10 UI 对偶）**、
//       自动化/告警编辑器的数据来源二选一、控制台无异常。
// ─────────────────────────────────────────────────────────────────────────────
test.describe('前端渲染：来源组（规则数据来源）', () => {
  test.beforeEach(async ({ page }) => {
    await seedSession(page)
    await mockAPI(page)
  })

  test('来源组列表：kind / combine / min_members / 成员 / active / revision 逐元素渲染', async ({ page }) => {
    await gotoPage(page, '/source-groups', /来源组/)

    const rows = page.locator('[data-test="sg-table"] .el-table__row')
    await expect(rows).toHaveCount(2)

    // 行 1：主备组
    const primary = rows.nth(0)
    await expect(primary.locator('[data-test="sg-name"]')).toHaveText('客厅光照主备')
    await expect(primary.locator('[data-test="sg-kind"]')).toHaveText('主备')
    await expect(primary.locator('[data-test="sg-category"]')).toHaveText('illumination')
    await expect(primary.locator('[data-test="sg-combine"]')).toHaveText('—')
    await expect(primary.locator('[data-test="sg-min-members"]')).toHaveText('—')
    await expect(primary.locator('[data-test="sg-members"]')).toHaveText('2 个成员')
    // active 必须来自 active_member_id（后端契约字段），不是 members.status 扫描的巧合
    await expect(primary.locator('[data-test="sg-active"]')).toHaveText('成员 #11（设备 2）')
    await expect(primary.locator('[data-test="sg-revision"]')).toHaveText('r7')

    // 行 2：复合组（combine/min_members 只在 composite 有意义）
    const composite = rows.nth(1)
    await expect(composite.locator('[data-test="sg-name"]')).toHaveText('1+2 光照合成')
    await expect(composite.locator('[data-test="sg-kind"]')).toHaveText('复合')
    await expect(composite.locator('[data-test="sg-combine"]')).toHaveText('平均（avg）')
    await expect(composite.locator('[data-test="sg-min-members"]')).toHaveText('≥ 1')
    await expect(composite.locator('[data-test="sg-members"]')).toHaveText('2 个成员')
    await expect(composite.locator('[data-test="sg-no-active"]')).toHaveText('复合组无 active')
    await expect(composite.locator('[data-test="sg-revision"]')).toHaveText('r3')
  })

  test('负例（SG-10 UI 对偶）：composite 组不得出现激活按钮，主备组必须有', async ({ page }) => {
    await gotoPage(page, '/source-groups', /来源组/)
    const rows = page.locator('[data-test="sg-table"] .el-table__row')
    await expect(rows).toHaveCount(2)
    // 主备组：激活入口存在（唯一）
    await expect(rows.nth(0).locator('[data-test="sg-activate"]')).toHaveCount(1)
    // 复合组：**硬断言 0** —— 语义上不存在"哪个成员权威"，后端 activate 亦返回 400
    await expect(rows.nth(1).locator('[data-test="sg-activate"]')).toHaveCount(0)
  })

  test('自动化规则编辑器：数据来源二选一 + 复合组 combine/min_members + 冷却提示', async ({ page }) => {
    await gotoPage(page, '/automation', /自动化|策略/)
    await page.locator('[data-test="create-rule"]').click()
    await expect(page.locator('[data-test="rule-dialog"]')).toBeVisible()

    // 默认（存量语义）：边缘设备下拉可见，来源组下拉不存在
    await expect(page.locator('[data-test="field-trigger-device"]')).toBeVisible()
    await expect(page.locator('[data-test="field-source-group"]')).toHaveCount(0)

    // 切到来源组
    await pickOption(page, 'field-source-kind', '来源组（主备 / 复合）')
    await expect(page.locator('[data-test="field-source-group"]')).toBeVisible()
    await expect(page.locator('[data-test="field-trigger-device"]')).toHaveCount(0)
    // 冷却下限提示（P-2：来源组规则 cooldown ≥ 成员采集间隔）
    await expect(page.locator('[data-test="cooldown-hint"]')).toContainText('冷却')

    // 选复合组：组语义显示 kind/combine/min_members
    await pickOption(page, 'field-source-group', '1+2 光照合成（复合）')
    const summary = page.locator('[data-test="source-group-summary"]')
    await expect(summary).toBeVisible()
    await expect(summary).toContainText('复合组')
    await expect(summary).toContainText('平均')
    await expect(summary).toContainText('最少 1 个成员')
    // 传感器名自动同步为组的数据类别（用户不需要再手打物理名）
    await expect(page.locator('input[data-test="field-sensor"]')).toHaveValue('illumination')

    // 切回边缘设备：设备下拉回来、组下拉消失
    await pickOption(page, 'field-source-kind', '边缘设备')
    await expect(page.locator('[data-test="field-trigger-device"]')).toBeVisible()
    await expect(page.locator('[data-test="field-source-group"]')).toHaveCount(0)
  })

  test('告警规则编辑器：选择来源组后目标设备字段隐藏、组选择可见', async ({ page }) => {
    await gotoPage(page, '/alerts', /告警/)
    await page.locator('[data-test="create-rule"]').click()
    await expect(page.locator('[data-test="rule-dialog"]')).toBeVisible()

    // 默认：存量目标字段可见
    await expect(page.locator('[data-test="field-target-id"]')).toBeVisible()

    await pickOption(page, 'field-source-kind', '来源组（主备 / 复合）')
    await expect(page.locator('[data-test="field-source-group"]')).toBeVisible()
    // 来源组规则不再要求用户选"目标设备"（成员由组定义）
    await expect(page.locator('[data-test="field-target-id"]')).toHaveCount(0)

    await pickOption(page, 'field-source-group', '客厅光照主备（主备）')
    await expect(page.locator('[data-test="source-group-summary"]')).toContainText('主备组')
  })

  test('自动化规则列表：来源组规则行标注来源组，不把双写锚点误显示成设备', async ({ page }) => {
    await gotoPage(page, '/automation', /自动化|策略/)
    const table = page.locator('[data-test="rules-table"]')
    await expect(table).toBeVisible()
    const rows = table.locator('.el-table__row')
    await expect(rows).toHaveCount(2)
    // 第 2 行 = 来源组规则（fixture AUTOMATION_RULES[1].source_group_id=7）
    await expect(rows.nth(1)).toContainText('来源组 #7')
    await expect(rows.nth(1)).toContainText('@soc')
  })

  test('渲染无未捕获异常：/source-groups 控制台无 error', async ({ page }) => {
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(String(e)))
    page.on('console', (m) => {
      if (m.type() === 'error') errors.push(m.text())
    })
    await gotoPage(page, '/source-groups', /来源组/)
    const real = errors.filter((e) => !/favicon|ResizeObserver|WebSocket|ERR_CONNECTION|Authentication failed/i.test(e))
    expect(real, `控制台错误：\n${real.join('\n')}`).toHaveLength(0)
  })
})
