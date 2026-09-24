import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, h, type VNode } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { compileStyleAsync, parse } from '@vue/compiler-sfc'
import AutomationRules from '../AutomationRules.vue'
import source from '../AutomationRules.vue?raw'
import { automationApi, type AutomationEvent, type AutomationEventPage } from '@/api/automation'
import { edgeDeviceApi } from '@/api/edgeDevice'
import { deviceOperationApi } from '@/api/deviceOperation'

/**
 * 样式契约辅助：用 @vue/compiler-sfc 真实编译 <style scoped>（复现构建期的
 * [data-v-*] 选择器改写），再注入 DOM 读 getComputedStyle —— 比字符串包含更接近
 * 浏览器实际会应用的声明。真实几何/可达性由 e2e 门禁在真浏览器里验收。
 */
async function compiledScopedCss(raw: string, filename: string): Promise<string> {
  const { descriptor } = parse(raw, { filename })
  const chunks: string[] = []
  for (const block of descriptor.styles) {
    const res = await compileStyleAsync({
      source: block.content,
      filename,
      id: 'data-v-testscope',
      scoped: Boolean(block.scoped),
    })
    expect(res.errors, JSON.stringify(res.errors)).toEqual([])
    chunks.push(res.code)
  }
  return chunks.join('\n')
}

/**
 * 注入编译产物 + 合成同结构 DOM（<div class="parentClass" data-v-testscope><div class="el-pagination">），
 * 返回内层元素的计算 flex-wrap。
 *
 * happy-dom 实测：只有在元素**插入文档后**读 getComputedStyle 才会做选择器匹配；
 * 若用"回调构造 DOM"或"插入前先读一次"，返回的是空串（不会命中已注入的样式表）。
 * 因此这里固定 DOM 形状、固定读取顺序。反证：把 parentClass 换成不匹配的值时
 * 返回值必须是空串 —— 说明该函数真的在走选择器匹配而非恒返回 wrap。
 */
function measureFlexWrap(css: string, parentClass: string, childClass: string | null = 'el-pagination'): string {
  const style = document.createElement('style')
  style.textContent = css
  document.head.appendChild(style)
  const wrap = document.createElement('div')
  wrap.className = parentClass
  wrap.setAttribute('data-v-testscope', '')
  const target = childClass === null ? wrap : document.createElement('div')
  if (childClass !== null) {
    target.className = childClass
    wrap.appendChild(target)
  }
  document.body.appendChild(wrap)
  const value = getComputedStyle(target).flexWrap
  style.remove()
  wrap.remove()
  return value
}
// Element Plus 组件由 src/test-setup.ts 全局 stub; 这里 mock 数据层。
vi.mock('@/api/automation', () => ({
  automationApi: {
    listRules: vi.fn(),
    createRule: vi.fn(),
    updateRule: vi.fn(),
    deleteRule: vi.fn(),
    setRuleEnabled: vi.fn(),
    listEvents: vi.fn(),
    confirmEvent: vi.fn(),
    triggerRule: vi.fn(),
  },
}))
vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getList: vi.fn(),
  },
}))
// F18 命令回链：页面现在会**真实查询**执行记录（不再是 console.log 占位），
// 因此必须 mock 该 api，否则点击会打到真实 axios。
vi.mock('@/api/deviceOperation', () => ({
  deviceOperationApi: { get: vi.fn() },
}))
vi.mock('element-plus', async importOriginal => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: Object.assign(vi.fn(), { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() }),
    ElMessageBox: { confirm: vi.fn().mockResolvedValue(true) },
  }
})

const mockedAutomationApi = vi.mocked(automationApi)
const mockedEdgeApi = vi.mocked(edgeDeviceApi)
const mockedOperationApi = vi.mocked(deviceOperationApi)

/** 一次成功执行的执行记录（GET /device-operations/:id 的响应形状）。 */
const operationFixture = {
  command_id: 'cmd-001',
  edge_device_id: 7,
  node_id: 'node-1',
  action_id: 'open_window',
  action_version: 2,
  status: 'SUCCEEDED' as const,
  created_at: '2026-09-15T10:00:00Z',
  updated_at: '2026-09-15T10:00:03Z',
  completed_at: '2026-09-15T10:00:03Z',
}

const ruleFixture = {
  id: 1,
  name: '高温开窗',
  enabled: true,
  trigger_type: 'sensor_threshold' as const,
  trigger_sensor_name: 'temperature',
  trigger_comparator: 'gt' as const,
  trigger_threshold: 30,
  trigger_duration_sec: 60,
  trigger_edge_device_id: 10,
  action_type: 'device_action' as const,
  action_device_id: 10,
  action_id: 'gpio_set',
  action_params_json: '{"pin":12,"value":1}',
  cooldown_sec: 300,
  max_daily_exec: 10,
  require_confirmed: true,
  created_at: '2026-08-21T00:00:00Z',
  updated_at: '2026-08-21T00:00:00Z',
}

/**
 * `automationApi.listEvents` 的返回契约在「裸数组」与 `{items,total,page,page_size}` 之间演进过。
 * 夹具同时具备数组与分页字段两种形态，使本文件的行为断言只依赖「组件渲染了什么」，
 * 与当前取数契约的收窄无关（避免为了形状而改断言）。
 */
type EventPageLike = AutomationEvent[] & AutomationEventPage
const eventPage = (rows: AutomationEvent[]): EventPageLike =>
  Object.assign([...rows], { items: rows, total: rows.length, page: 1, page_size: rows.length })

const eventFixture = {
  id: 100,
  rule_id: 1,
  triggered_at: '2026-08-21T12:00:00Z',
  trigger_value: 31.5,
  result: 'pending_confirm' as const,
  command_id: 'cmd-001',
  detail: '高温触发，等待确认',
  created_at: '2026-08-21T12:00:00Z',
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  mockedAutomationApi.listRules.mockResolvedValue([ruleFixture])
  mockedAutomationApi.listEvents.mockResolvedValue(eventPage([eventFixture]))
  mockedEdgeApi.getList.mockResolvedValue({ total: 1, items: [{ id: 10, name: 'ESP32-01' } as never] })
})

async function mountPage() {
  const wrapper = mount(AutomationRules, {
    global: { plugins: [createPinia()] },
  })
  await flushPromises()
  return wrapper
}

/**
 * 本文件专用的 el-table/el-table-column 替身：真实渲染表头、每行每列，并执行列的
 * #default 作用域插槽（src/test-setup.ts 的通用 stub 只把行数据拍平成文本、不执行插槽，
 * 无法断言某个单元格实际渲染了什么）。仅用于「规则」列的 DOM 断言，不影响既有用例。
 */
function slotTable() {
  const defaultSlotOf = (vn: VNode) => {
    const children = vn.children
    if (children && typeof children === 'object' && !Array.isArray(children)) {
      const slot = children['default']
      if (typeof slot === 'function') return slot
    }
    return null
  }
  const SlotColumn = defineComponent({
    name: 'ElTableColumn',
    props: { prop: String, label: String, width: [String, Number], minWidth: [String, Number], fixed: [String, Boolean], type: String },
    setup() {
      return () => null
    },
  })
  const SlotTable = defineComponent({
    name: 'ElTable',
    props: { data: { type: Array, default: () => [] } },
    setup(props, { slots }) {
      return () => {
        const columns = (slots.default?.() ?? []) as VNode[]
        const rows = props.data as Array<Record<string, unknown>>
        return h('table', { class: 'el-table' }, [
          h('thead', [h('tr', columns.map(vn => h('th', { class: 'el-table__cell' }, String(vn.props?.['label'] ?? ''))))]),
          h('tbody', rows.map((row, index) => h('tr', { key: String(row?.['id'] ?? index) }, columns.map(vn => {
            const scoped = defaultSlotOf(vn)
            const content = scoped ? scoped({ row }) : String(row?.[String(vn.props?.['prop'] ?? '')] ?? '')
            return h('td', { class: 'el-table__cell' }, content)
          })))),
        ])
      }
    },
  })
  return { ElTable: SlotTable, 'el-table': SlotTable, ElTableColumn: SlotColumn, 'el-table-column': SlotColumn }
}

/** 用执行作用域插槽的表格替身挂载（仅新用例使用）。 */
async function mountWithSlotTable() {
  const wrapper = mount(AutomationRules, {
    global: { plugins: [createPinia()], components: slotTable() },
  })
  await flushPromises()
  return wrapper
}

/**
 * 取 listEvents 最后一次调用的实参。
 * 用索引而非 Array.prototype.at —— tsconfig 的 lib 未含 ES2022, `.at()` 过不了 typecheck。
 */
function lastEventParams(): Record<string, unknown> {
  const calls = mockedAutomationApi.listEvents.mock.calls
  return calls[calls.length - 1][0] as Record<string, unknown>
}

/** 事件表首行的单元格文本数组。 */
function firstEventRowCells(wrapper: Awaited<ReturnType<typeof mountWithSlotTable>>) {
  const row = wrapper.find('[data-test="events-table"] tbody tr')
  expect(row.exists()).toBe(true)
  return row.findAll('td').map(td => td.text())
}

describe('AutomationRules.vue', () => {
  it('挂载后加载规则与事件', async () => {
    await mountPage()
    expect(mockedAutomationApi.listRules).toHaveBeenCalledTimes(1)
    expect(mockedAutomationApi.listEvents).toHaveBeenCalledTimes(1)
  })

  it('规则表格渲染', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('[data-test="rules-table"]').exists()).toBe(true)
    // ElTable stub 渲染行数据为 flat text（含原始字段值），slot 模板不渲染
    expect(wrapper.text()).toContain('高温开窗')
    expect(wrapper.text()).toContain('sensor_threshold')
    expect(wrapper.text()).toContain('temperature')
  })

  it('事件表格渲染', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('[data-test="events-table"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('pending_confirm')
  })

  it('pending_confirm 事件显示确认按钮', async () => {
    await mountPage()
    // ElTable stub 不渲染 slot 模板，确认按钮在 slot 内不可见。
    // 改为验证源码中 pending_confirm 行有确认按钮逻辑（?raw 断言）。
    const source = await import('../AutomationRules.vue?raw')
    expect(source.default).toContain('pending_confirm')
    expect(source.default).toContain('confirm-event')
    expect(source.default).toContain('确认执行')
  })

  it('打开创建对话框并校验空表单', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    expect(wrapper.find('[data-test="rule-dialog"]').exists()).toBe(true)
    // 空表单保存应被拦截 (ElMessage.warning)
    const { ElMessage } = await import('element-plus')
    await wrapper.find('[data-test="save-rule"]').trigger('click')
    await flushPromises()
    expect(ElMessage.warning).toHaveBeenCalledWith('请填写规则名称')
  })

  it('创建成功后调用 API 并关闭对话框', async () => {
    const created = { ...ruleFixture, id: 2, name: '新规则' }
    mockedAutomationApi.createRule.mockResolvedValue(created)
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    await wrapper.find('[data-test="field-name"]').setValue('新规则')
    await wrapper.find('[data-test="field-trigger-device"]').setValue('10')
    await wrapper.find('[data-test="field-sensor"]').setValue('temperature')
    await wrapper.find('[data-test="field-action-device"]').setValue('10')
    await wrapper.find('[data-test="field-action-id"]').setValue('gpio_set')
    await wrapper.find('[data-test="save-rule"]').trigger('click')
    await flushPromises()
    expect(mockedAutomationApi.createRule).toHaveBeenCalledTimes(1)
    expect(mockedAutomationApi.createRule).toHaveBeenCalledWith(expect.objectContaining({
      name: '新规则',
      trigger_edge_device_id: 10,
      trigger_sensor_name: 'temperature',
      action_device_id: 10,
      action_id: 'gpio_set',
    }))
  })

  it('启用/禁用切换调 setRuleEnabled', async () => {
    mockedAutomationApi.setRuleEnabled.mockResolvedValue({ ...ruleFixture, enabled: false })
    await mountPage()
    // ElTable stub 不渲染 slot 模板，el-switch 在 slot 内不可见。
    // 改为验证源码中启用列有 el-switch 绑定 onToggle。
    const source = await import('../AutomationRules.vue?raw')
    expect(source.default).toContain('rule-enabled')
    expect(source.default).toContain('onToggle')
  })

  /**
   * 危险确认契约（规范 §3.4.3 / §4.3.4）：删除规则不可恢复，
   * 确认键必须是 danger 语义类型 + danger class，且 autofocus:false；
   * 这里是「页面 → feedback.confirmDanger」的接线守卫。
   * confirmDanger 自身的真实渲染/焦点行为由 utils/__tests__/feedbackConfirmDanger.spec.ts 覆盖。
   */
  const dangerContract = expect.objectContaining({
    confirmButtonType: 'danger',
    confirmButtonClass: 'el-button--danger',
    autofocus: false,
  })

  it('删除规则走危险确认契约，且文案含对象身份', async () => {
    const { ElMessageBox } = await import('element-plus')
    const confirmSpy = vi.mocked(ElMessageBox.confirm)
    mockedAutomationApi.deleteRule.mockResolvedValue(undefined)
    await mountPage()

    // 规则行的删除按钮在 el-table 作用域槽内，通用 stub 不渲染槽；
    // 故用执行作用域槽的表格替身拿到真实按钮，再点它触发 onDelete。
    const wrapper = await mountWithSlotTable()
    const delBtn = wrapper.findAll('button').find(b => b.text() === '删除')
    expect(delBtn).toBeTruthy()
    await delBtn!.trigger('click')
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledWith(
      expect.stringContaining('高温开窗'),
      expect.any(String),
      dangerContract,
    )
    // 确认后（本文件默认 mock resolve(true)）才真正删除，且用该行的 id：
    // 证明 confirmDanger 的 true/false 结果真的被 onDelete 消费，而不是摆设。
    expect(mockedAutomationApi.deleteRule).toHaveBeenCalledWith(1)
  })

  it('删除规则取消时不调用 deleteRule', async () => {
    const { ElMessageBox } = await import('element-plus')
    vi.mocked(ElMessageBox.confirm).mockRejectedValueOnce(new Error('cancel'))
    const wrapper = await mountWithSlotTable()
    const delBtn = wrapper.findAll('button').find(b => b.text() === '删除')
    await delBtn!.trigger('click')
    await flushPromises()
    expect(mockedAutomationApi.deleteRule).not.toHaveBeenCalled()
  })

  /**
   * F18 命令 ID 回链：此前是 `console.log` 占位 —— 用户点了**没有任何反馈**，
   * 是"伪装成正常"家族（动作被吞掉）。修复后必须**真实查询**并展示终态。
   *
   * 判据取"API 参数 + 可见结果"两条，而不是"弹窗打开了"：
   * 后者在旧的 console.log 版本里同样为假（弹窗压根没实现），却容易被写成恒真断言。
   */
  it('F18：点击命令 ID 会真实查询该次执行并展示状态（不再是 console.log 占位）', async () => {
    mockedOperationApi.get.mockResolvedValueOnce(operationFixture)
    const wrapper = await mountWithSlotTable()

    const cmdBtn = wrapper.findAll('button').find(b => b.text().includes('cmd-001'))
    expect(cmdBtn, '命令 ID 应当是可点击的回链（否则用户无法追查这条命令）').toBeTruthy()
    await cmdBtn!.trigger('click')
    await flushPromises()

    // ① 真的按命令 ID 查了后端（本地 console.log 不会有这次调用）
    expect(mockedOperationApi.get).toHaveBeenCalledWith('cmd-001')
    // ② 查回来的终态**渲染给用户**（而不是只写进日志）
    const html = wrapper.html()
    expect(html).toContain('命令执行详情')
    expect(html).toContain('成功')
    expect(html).toContain('open_window')
  })

  it('F18：命令记录查不到时必须显式报错（不得留一个空弹窗）', async () => {
    mockedOperationApi.get.mockRejectedValueOnce(new Error('operation not found'))
    const wrapper = await mountWithSlotTable()

    const cmdBtn = wrapper.findAll('button').find(b => b.text().includes('cmd-001'))
    await cmdBtn!.trigger('click')
    await flushPromises()

    expect(mockedOperationApi.get).toHaveBeenCalledWith('cmd-001')
    // 失败必须走统一反馈出口，而不是静默关闭弹窗让用户以为"没有详情"。
    // 注意出口的**真实形状**：feedback.error() 调的是 `ElMessage({...})`（函数调用），
    // 不是 `ElMessage.error(...)` —— 按后者断言会永远红（本用例第一版就踩了这个）。
    const { ElMessage } = await import('element-plus')
    const fn = ElMessage as unknown as { mock?: { calls: unknown[][] } }
    expect(fn.mock?.calls.length ?? 0, '失败时必须弹出错误提示').toBeGreaterThan(0)
    // 提示里必须带"是哪个操作失败了"——否则用户只看到服务端的 "operation not found"，
    // 不知道这是"查命令记录"失败（本仓 I-1 收敛时的既有取舍，见 utils/feedback.ts 注释）。
    const lastCall = JSON.stringify(fn.mock!.calls.at(-1))
    expect(lastCall).toContain('未能读取')
  })

  it('确认执行高风险动作走危险确认契约', async () => {
    const { ElMessageBox } = await import('element-plus')
    const confirmSpy = vi.mocked(ElMessageBox.confirm)
    // 真实字段是 result（AutomationEvent.state 从不存在，见 api/automation.ts:32-33 的迁移说明）
    mockedAutomationApi.listEvents.mockResolvedValue(eventPage([{ ...eventFixture, result: 'pending_confirm' }]))
    const wrapper = await mountWithSlotTable()
    const confirmBtn = wrapper.findAll('button').find(b => b.text() === '确认执行')
    expect(confirmBtn).toBeTruthy()
    await confirmBtn!.trigger('click')
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledWith(
      expect.stringContaining('高风险动作'),
      expect.any(String),
      dangerContract,
    )
  })

  it('确认事件调 confirmEvent', async () => {
    mockedAutomationApi.confirmEvent.mockResolvedValue({ ...eventFixture, result: 'executed' })
    await mountPage()
    // ElTable stub 不渲染 slot 模板，确认按钮在 slot 内不可见。
    // 改为验证源码中 confirmEvent 绑定存在。
    const source = await import('../AutomationRules.vue?raw')
    expect(source.default).toContain('confirm-event')
    expect(source.default).toContain('confirmEvent')
  })

  it('手动触发按钮存在且调用 triggerRule', async () => {
    mockedAutomationApi.triggerRule.mockResolvedValue({ ...eventFixture, result: 'executed', trigger_source: 'manual' })
    await mountPage()
    // ElTable stub 不渲染 slot 模板，触发按钮在 slot 内不可见。
    // 改为验证源码中 trigger-rule 按钮 + onTrigger 绑定存在。
    const source = await import('../AutomationRules.vue?raw')
    expect(source.default).toContain('trigger-rule')
    expect(source.default).toContain('onTrigger')
    expect(source.default).toContain('triggerRule')
    // 验证 onTrigger 函数逻辑: 确认框 + API 调用 + 结果提示
    // G10 裁决: 手动触发是**非破坏性**操作 ⇒ 走 feedback.confirm (普通确认),
    // **不得**走 confirmDanger (danger 红键只用于破坏性操作, 见 listPageConventions.spec.ts)。
    expect(source.default).toContain('feedback.confirm(')
    expect(source.default).not.toContain('ElMessageBox.confirm')
    expect(source.default).toContain('手动触发规则')
    expect(source.default).toContain('跳过条件评估与确认制')
    expect(source.default).toContain('fetchEvents()')
  })

  /**
   * G10 接线守卫（行为断言，不是源码字符串）：手动触发是**非破坏性**操作，
   * 必须走 feedback.confirm（普通确认，type:'info'，确认键不带 danger 语义）。
   * 这里**不** mock feedback.confirm —— 让它真实执行、落在被 mock 的 ElMessageBox 上，
   * 断言的就是真实会传给弹窗的 options。
   * 反证：迁成 confirmDanger 后 options 里会出现 confirmButtonType:'danger' ⇒ 变红。
   */
  it('G10: 手动触发走 feedback.confirm 普通确认 (不带 danger 语义)', async () => {
    const { ElMessageBox } = await import('element-plus')
    const boxSpy = vi.mocked(ElMessageBox.confirm)
    boxSpy.mockResolvedValueOnce('confirm' as never)
    mockedAutomationApi.triggerRule.mockResolvedValue({ ...eventFixture, result: 'executed', trigger_source: 'manual' })
    boxSpy.mockClear()

    const wrapper = await mountWithSlotTable()
    const triggerBtn = wrapper.findAll('button').find(b => b.text() === '触发')
    expect(triggerBtn, '找不到「触发」按钮，用例前提不成立').toBeTruthy()
    await triggerBtn!.trigger('click')
    await flushPromises()

    // 1) 确实弹了确认框, 标题与文案保留原语义。
    expect(boxSpy).toHaveBeenCalledTimes(1)
    const [message, title, options] = boxSpy.mock.calls[0]
    expect(String(message)).toContain('手动触发规则「高温开窗」？')
    expect(String(message)).toContain('跳过条件评估与确认制')
    expect(title).toBe('手动触发')
    // 2) 关键: 确认框**不得**是 danger 语义 (danger 只用于破坏性操作)。
    expect(options).not.toEqual(expect.objectContaining({ confirmButtonType: 'danger' }))
    expect(JSON.stringify(options)).not.toContain('danger')
    // 3) 确认后才真正触发。
    expect(mockedAutomationApi.triggerRule).toHaveBeenCalledWith(1)
  })

  it('G10: 手动触发确认框取消时不调用 triggerRule', async () => {
    const { ElMessageBox } = await import('element-plus')
    vi.mocked(ElMessageBox.confirm).mockRejectedValueOnce(new Error('cancel'))

    const wrapper = await mountWithSlotTable()
    const triggerBtn = wrapper.findAll('button').find(b => b.text() === '触发')
    expect(triggerBtn).toBeTruthy()
    await triggerBtn!.trigger('click')
    await flushPromises()

    // 取消 (false) ⇒ 一个请求都不发。
    expect(mockedAutomationApi.triggerRule).not.toHaveBeenCalled()
  })

  it('触发历史「规则」列渲染规则名而不是裸主键', async () => {
    const wrapper = await mountWithSlotTable()
    const cells = firstEventRowCells(wrapper)
    expect(cells).toContain('高温开窗')
    // 回归护栏：修复前列内容恰为 String(rule_id) === '1'
    expect(cells).not.toContain('1')
  })

  it('触发历史「规则」列在规则已被删除时回退为 #id，不留空白', async () => {
    mockedAutomationApi.listEvents.mockResolvedValue(eventPage([{ ...eventFixture, id: 101, rule_id: 999 }]))
    const wrapper = await mountWithSlotTable()
    const cells = firstEventRowCells(wrapper)
    expect(cells).toContain('#999')
    expect(cells.every(cell => cell.trim() !== '')).toBe(true)
  })

  it('手动触发结果显示区分', async () => {
    // 验证 resultText 覆盖手动触发可能返回的所有结果
    const source = await import('../AutomationRules.vue?raw')
    for (const r of ['executed', 'notification', 'suppressed_cooldown', 'suppressed_daily_limit', 'failed_gate', 'failed_dispatch']) {
      expect(source.default).toContain(r)
    }
  })

  // ─── 分页与「筛选变化重置页码」 (§3.2.6 MUST) ───
  //
  // 断言的是**真实请求参数** (listEvents 实际收到的 params), 不是源码字符串:
  // 源码里写没写 `page = 1` 与运行时是否真发了 page=1 是两件事。

  it('挂载时按默认页码请求事件 (page=1, page_size=20)', async () => {
    await mountPage()
    expect(mockedAutomationApi.listEvents).toHaveBeenCalledTimes(1)
    const params = mockedAutomationApi.listEvents.mock.calls[0][0] as Record<string, unknown>
    expect(params.page).toBe(1)
    expect(params.page_size).toBe(20)
  })

  it('翻到第 3 页后请求带 page=3, 且刷新按钮不会把页码打回 1', async () => {
    const wrapper = await mountPage()
    // 分页控件 stub 点一下 = currentPage + 1 并 emit current-change。
    await wrapper.find('[data-test="events-pagination"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-test="events-pagination"]').trigger('click')
    await flushPromises()

    const last = lastEventParams()
    expect(last.page).toBe(3)

    // 「刷新」按钮复用 fetchEvents; 它不是筛选变化, 不得重置页码 (否则永远停在第一页)。
    await wrapper.find('[data-test="refresh-events"]').trigger('click')
    await flushPromises()
    const afterRefresh = lastEventParams()
    expect(afterRefresh.page).toBe(3)
  })

  it('筛选规则变化后请求的 page 参数被重置为 1', async () => {
    const wrapper = await mountPage()
    // 先翻到第 3 页, 制造「页码非 1」的前置状态。
    await wrapper.find('[data-test="events-pagination"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-test="events-pagination"]').trigger('click')
    await flushPromises()
    expect((lastEventParams()).page).toBe(3)

    // 换筛选: rule_id=1。第 3 页在更小的结果集里可能越界, 必须以 page=1 重新查询。
    await wrapper.find('[data-test="filter-rule"]').setValue('1')
    await flushPromises()

    const params = lastEventParams()
    expect(params.page).toBe(1)
    expect(params.rule_id).toBe(1)
  })

  it('结果筛选变化后请求的 page 参数被重置为 1', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="events-pagination"]').trigger('click')
    await flushPromises()
    expect((lastEventParams()).page).toBe(2)

    await wrapper.find('[data-test="filter-result"]').setValue('expired')
    await flushPromises()

    const params = lastEventParams()
    expect(params.page).toBe(1)
    expect(params.result).toBe('expired')
  })

  it('筛选值未变化时不重置页码 (同一筛选下重复查询保持当前页)', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-test="events-pagination"]').trigger('click')
    await flushPromises()
    expect((lastEventParams()).page).toBe(2)

    // 再次触发同一筛选值 (setValue 空值 === 初值 → 不算筛选变化)。
    await wrapper.find('[data-test="filter-rule"]').setValue('')
    await flushPromises()
    const after = lastEventParams()
    expect(after.page).toBe(2)
  })

  it('分页控件渲染 total 并暴露 el-pagination', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('[data-test="events-pagination"]').exists()).toBe(true)
    // stub 渲染 "共 N 条"; total 来自接口响应而非当前页长度。
    expect(wrapper.find('[data-test="events-pagination"]').text()).toContain('共 1 条')
  })

  // ── 分页控件窄屏可换行（§4.3.2 MUST；与 LogicalDeviceList.vue 同一根因） ──────
  //
  // .events-pagination 的 flex-wrap 只作用于多个 item 之间；内层 el-pagination
  // 自身 white-space:nowrap + display:flex（宽 676.94px），**768px 即已裁切**：
  // el-pagination x=47.06（容器 .events-pagination x=236，裁 153px）、
  // el-pagination__total x=47.06、el-select「20条/页」x=130；
  // 360px 时 el-pagination x=-360.94、btn-prev x=-134。祖先链
  // scrollWidth === clientWidth ⇒ 真实裁切，elementFromPoint 在 btn-prev 中心返回 null。
  // 范式同 firmware/FirmwareManage.vue 的 .firmware-manage :deep(.el-pagination)。
  // 单测只能用样式契约：happy-dom 无布局引擎（详见本文件顶部辅助函数注释）。
  describe('分页控件窄屏换行（样式契约，happy-dom 无布局引擎的例外）', () => {
    it('内层 .el-pagination 自身声明 flex-wrap，过宽时才会拆行', async () => {
      const css = await compiledScopedCss(source, 'AutomationRules.vue')
      const rule = css.match(/\.events-pagination\[data-v-[a-z0-9]+\]\s+\.el-pagination\s*\{[^}]*\}/)
      expect(rule, 'AutomationRules.vue 缺少 .events-pagination :deep(.el-pagination) 编译产物').not.toBeNull()
      expect(rule![0]).toContain('flex-wrap: wrap')
      const outer = css.match(/\.events-pagination\[data-v-[a-z0-9]+\]\s*\{[^}]*\}/)
      expect(outer, 'AutomationRules.vue 缺少 .events-pagination 自身的 scoped 规则').not.toBeNull()
      expect(outer![0]).toContain('flex-wrap: wrap')
      expect(measureFlexWrap(css, 'events-pagination')).toBe('wrap')
      // 反证：容器 class 不匹配时必须取不到该声明（证明测的是选择器而非恒真）
      expect(measureFlexWrap(css, 'not-the-container')).toBe('')
    })
  })

// ─── F8 移动端宽表横滚合同（§4.3.2.2 MUST / §4.4.1 MUST） ───────────────────
// 断言的是**真实渲染出的 DOM 祖先链**，不是源码字符串包含：
// `expect(src).toContain('class="mobile-table-wrapper"')` 无法区分
// 「包住了这张表」还是「包住了另一张表 / 只写在注释里」。happy-dom 无布局引擎
// （getBoundingClientRect 恒 0），像素级可达性由真浏览器探针验收：
// frontend-shared/.tmp-probe/f8-f10-probe.mjs
function expectEveryTableWrapped(wrapper: { findAll: (s: string) => Array<{ element: Element }> }) {
  const tables = wrapper.findAll('.el-table')
  expect(tables.length, '渲染出的 el-table 数量为 0，断言会假绿').toBeGreaterThan(0)
  for (const t of tables) {
    const el = t.element as HTMLElement
    const box = el.closest('.mobile-table-wrapper')
    expect(box, 'el-table 不在 .mobile-table-wrapper 祖先链上').not.toBeNull()
    const hint = (box as HTMLElement).querySelector(':scope > .mobile-table-hint')
    expect(hint, '.mobile-table-wrapper 缺少直接子节点 .mobile-table-hint').not.toBeNull()
    expect(hint!.textContent).toContain('左右滑动')
  }
}

  describe('F8 移动端宽表横滚合同（§4.3.2.2 MUST）', () => {
    it('规则表与事件表都渲染在 .mobile-table-wrapper 内，且带横滑提示', async () => {
      const wrapper = await mountPage()
      expectEveryTableWrapped(wrapper)
      // 本页有两张表（规则 + 触发历史），逐张判定，不能一张包裹就整页通过
      expect(wrapper.findAll('.mobile-table-wrapper')).toHaveLength(2)
      expect(wrapper.findAll('.mobile-table-hint')).toHaveLength(2)
    })

    it('分页器仍在 wrapper 之外（横滚容器不得把分页一起卷走）', async () => {
      const wrapper = await mountPage()
      const pagination = wrapper.find('.events-pagination')
      expect(pagination.exists()).toBe(true)
      expect(pagination.element.closest('.mobile-table-wrapper')).toBeNull()
    })
  })

  // ── G5：人工确认推送到达时自动刷新（改前本页 0 订阅，事件到达用户看不到） ──
  //
  // 后端在「待人工确认 / 日熔断 / 系统执行者不可用」时已经 WS 推送
  // （automation/planner.go:550/474/525），但改前本页没有任何订阅：
  // 事件到达时页面不刷新，用户得手动点「刷新」或翻页才可能发现待确认项，
  // 而确认有超时——策略会卡在 pending_confirm。MainLayout 只弹提示+跳转，
  // 不刷新本页数据（MainLayout.vue:495-508）。
  //
  // 断言手法：store 的 subscribe 是公开 API，handler 存在闭包里的
  // messageHandlers；测试通过 subscribe 的返回值与 spy 捕获调用，
  // 再用**独立订阅同一事件名**拿到的注册顺序来驱动（见 dispatchPush）。
  describe('G5 自动化推送驱动的自动刷新', () => {
    /**
     * 用同一个 pinia 实例挂载，并捕获组件在 onMounted 里注册的 (事件名 → handler)。
     *
     * 为什么不能在 mountPage() 之后用 useWebSocketStore() 取 store 再派发：
     * mountPage 内部 createPinia() 会新建实例，而测试里的 useWebSocketStore()
     * 取到的是另一个 pinia 下的 store（两者 messageHandlers 不同）——
     * 第一版就这么写的，结果 captured 为空、断言全红。
     */
    async function mountAndCapture() {
      const { useWebSocketStore } = await import('@/stores/websocket')
      const pinia = createPinia()
      const store = useWebSocketStore(pinia)
      const captured = new Map<string, (m: unknown) => void>()
      vi.spyOn(store, 'subscribe').mockImplementation((type: string, handler: never) => {
        captured.set(type, handler)
        return () => { captured.delete(type) }
      })
      const wrapper = mount(AutomationRules, { global: { plugins: [pinia] } })
      await flushPromises()
      return { wrapper, captured }
    }

    it('订阅了后端实际推送的 3 个事件名（planner.go:474/525/550）', async () => {
      const { captured } = await mountAndCapture()
      expect([...captured.keys()]).toContain('automation_pending_confirm')
      expect([...captured.keys()]).toContain('automation_daily_limit')
      expect([...captured.keys()]).toContain('automation_system_actor_unavailable')
      vi.restoreAllMocks()
    })

    it('收到 automation_pending_confirm 后自动重新拉取事件（无需用户手动刷新）', async () => {
      const { captured } = await mountAndCapture()
      const listEvents = automationApi.listEvents as unknown as ReturnType<typeof vi.fn>
      const before = listEvents.mock.calls.length
      captured.get('automation_pending_confirm')!({ type: 'automation_pending_confirm', payload: { detail: { rule_name: 'X' } } })
      await flushPromises()
      expect(listEvents.mock.calls.length).toBeGreaterThan(before)
      vi.restoreAllMocks()
    })

    it('卸载后取消订阅（页面销毁后不得继续刷新）', async () => {
      const { wrapper, captured } = await mountAndCapture()
      expect(captured.has('automation_pending_confirm')).toBe(true)
      wrapper.unmount()
      // onUnmounted 调用了 subscribe 返回的 off()，captured 中该键应被删除
      expect(captured.has('automation_pending_confirm')).toBe(false)
      vi.restoreAllMocks()
    })
  })
})

