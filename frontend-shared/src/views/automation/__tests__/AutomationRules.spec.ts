import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, h, type VNode } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { compileStyleAsync, parse } from '@vue/compiler-sfc'
import AutomationRules from '../AutomationRules.vue'
import source from '../AutomationRules.vue?raw'
import { automationApi, type AutomationEvent, type AutomationEventPage, type PrecheckResponse } from '@/api/automation'
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
    precheckRule: vi.fn(),
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
  // actions() 是新的动作目录来源（风险徽标读后端下发的 requires_confirmation）。
  // 不 mock 会让页面挂载时打到真实 axios。
  deviceOperationApi: { get: vi.fn(), actions: vi.fn() },
}))
vi.mock('element-plus', async importOriginal => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: Object.assign(vi.fn(), { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() }),
    ElMessageBox: { confirm: vi.fn().mockResolvedValue(true) },
  }
})

/**
 * 2026-10-21 第二轮：窄屏改卡片列表后，模板按 useResponsive().isMobile 走结构分支。
 * 这里直接驱动模块级共享 ref（与 AlertRules.spec / FixedColumnOcclusion.spec 同法），
 * 默认 1440/桌面 —— 既有 55 条用例的语义完全不变。
 */
const { viewportWidth, isMobileRef } = vi.hoisted(() => {
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  const { ref } = require('vue') as typeof import('vue')
  return { viewportWidth: ref(1440), isMobileRef: ref(false) }
})
vi.mock('@/composables/useResponsive', () => ({
  BREAKPOINTS: { xs: 480, sm: 640, md: 768, lg: 1024, xl: 1280, '2xl': 1536 },
  useResponsive: () => ({
    width: viewportWidth,
    isMobile: isMobileRef,
    isTablet: { value: false },
    isDesktop: { value: true },
  }),
}))

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

/** 设备 10 的动作目录：一个风险动作（requires_confirmation=true）+ 一个普通动作。 */
const deviceActionsFixture = [
  { definition: { id: 'set_mos_policy', name: '设置充放电 MOS 软件策略', description: '改变充放电 MOS 软件关闭位', risk_level: 'elevated', requires_confirmation: true }, available: true },
  { definition: { id: 'read_basic_info', name: '读取基本信息', description: '读取总压/电流', risk_level: 'normal', requires_confirmation: false }, available: true },
]

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  // 每个用例从桌面视口起步，避免上一条窄屏用例污染后续断言
  viewportWidth.value = 1440
  isMobileRef.value = false
  mockedAutomationApi.listRules.mockResolvedValue([ruleFixture])
  mockedAutomationApi.listEvents.mockResolvedValue(eventPage([eventFixture]))
  mockedEdgeApi.getList.mockResolvedValue({ total: 1, items: [{ id: 10, name: 'ESP32-01' } as never] })
  // 默认预检：无风险动作（既有用例大多走普通路径，保持它们的行为不变）
  mockedAutomationApi.precheckRule.mockResolvedValue({ risk_actions: [], blocking_errors: [], warnings: [] })
  mockedOperationApi.actions.mockResolvedValue(deviceActionsFixture as never)
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

  it('创建成功后调用 API 并关闭对话框（多动作契约: 提交 actions 数组, seq 由下标派生）', async () => {
    const created = { ...ruleFixture, id: 2, name: '新规则' }
    mockedAutomationApi.createRule.mockResolvedValue(created)
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    await wrapper.find('[data-test="field-name"]').setValue('新规则')
    await wrapper.find('[data-test="field-trigger-device"]').setValue('10')
    await wrapper.find('[data-test="field-sensor"]').setValue('temperature')
    // 单动作字段组已升级为卡片列表：目标是第一张卡片（下标 0）
    await wrapper.find('[data-test="action-device-0"]').setValue('10')
    await wrapper.find('[data-test="action-id-0"]').setValue('read_basic_info')
    await wrapper.find('[data-test="save-rule"]').trigger('click')
    await flushPromises()
    expect(mockedAutomationApi.createRule).toHaveBeenCalledTimes(1)
    expect(mockedAutomationApi.createRule).toHaveBeenCalledWith(expect.objectContaining({
      name: '新规则',
      trigger_edge_device_id: 10,
      trigger_sensor_name: 'temperature',
      actions: [{ seq: 1, action_type: 'device_action', action_device_id: 10, action_id: 'read_basic_info' }],
    }))
  })

  it('保存前先走 precheck（风险清单的判定源在后端，前端不自己算）', async () => {
    mockedAutomationApi.createRule.mockResolvedValue({ ...ruleFixture, id: 3 })
    const wrapper = await mountPage()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    await wrapper.find('[data-test="field-name"]').setValue('普通规则')
    await wrapper.find('[data-test="field-trigger-device"]').setValue('10')
    await wrapper.find('[data-test="field-sensor"]').setValue('temperature')
    await wrapper.find('[data-test="action-device-0"]').setValue('10')
    await wrapper.find('[data-test="action-id-0"]').setValue('read_basic_info')
    await wrapper.find('[data-test="save-rule"]').trigger('click')
    await flushPromises()

    expect(mockedAutomationApi.precheckRule).toHaveBeenCalledTimes(1)
    // 预检先于落库：删掉 precheck 调用本断言必红
    expect(mockedAutomationApi.precheckRule.mock.invocationCallOrder[0])
      .toBeLessThan(mockedAutomationApi.createRule.mock.invocationCallOrder[0])
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
      vi.spyOn(store, 'subscribe').mockImplementation(((type: string, handler: (m: never) => void) => {
        captured.set(type, handler as (m: unknown) => void)
        return () => { captured.delete(type) }
      }) as typeof store.subscribe)
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

  // ══════════════════════════════════════════════════════════════════════════
  // B4 门禁 F-1..F-10（方案 §8.2）
  //
  // 断言口径：能读**运行时行为**就不读源码字符串 —— 字符串包含在"代码存在但
  // 从不被调用"时同样为绿（祖父化）。少数只能靠源码契约的（例如"不得出现某写法"）
  // 显式标注理由。
  // ══════════════════════════════════════════════════════════════════════════

  /** 打开创建对话框并填到"可保存"的最小状态（第一张卡片用普通动作）。 */
  async function openCreateFilled(wrapper: Awaited<ReturnType<typeof mountPage>>, name = '测试规则') {
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    await wrapper.find('[data-test="field-name"]').setValue(name)
    await wrapper.find('[data-test="field-trigger-device"]').setValue('10')
    await wrapper.find('[data-test="field-sensor"]').setValue('temperature')
    await wrapper.find('[data-test="action-device-0"]').setValue('10')
    await wrapper.find('[data-test="action-id-0"]').setValue('read_basic_info')
  }

  /** 预检返回一个风险动作（含后端给的设备名/标签/原因/参数摘要）。 */
  function riskPrecheck(): PrecheckResponse {
    return {
      risk_actions: [{
        seq: 1,
        action_id: 'set_mos_policy',
        action_device_id: 10,
        action_device_name: 'BMS-01',
        risk_level: 'elevated',
        risk_label: '风险',
        risk_reason: '关闭充放电 MOS 会切断电池充放电回路',
        params_summary: 'charge_software_closed=false, discharge_software_closed=true',
      }],
      blocking_errors: [],
      warnings: [],
    }
  }

  describe('F-1 日出/日落触发器', () => {
    it('触发器下拉含「日出」「日落」，且选择后出现偏移分钟字段与实时文案', async () => {
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      const options = wrapper.find('[data-test="field-trigger-type"]').findAll('option').map(o => o.text())
      expect(options).toContain('日出')
      expect(options).toContain('日落')
      // 「事件驱动」仍是 disabled（既有行为不得被顺手改掉）
      const disabled = wrapper.find('[data-test="field-trigger-type"]').findAll('option').filter(o => o.attributes('disabled') !== undefined)
      expect(disabled.length).toBe(1)

      // 选「日落」→ 日出/日落字段组出现
      await wrapper.find('[data-test="field-trigger-type"]').setValue('sunset')
      await flushPromises()
      expect(wrapper.find('[data-test="field-sun-offset"]').exists()).toBe(true)
      expect(wrapper.find('[data-test="offset-sentence"]').text()).toBe('日落正点')

      // 填 -30 → 回显"日落前 30 分钟"（用户需求原文）
      await wrapper.find('[data-test="field-sun-offset"]').setValue('-30')
      await flushPromises()
      expect(wrapper.find('[data-test="offset-sentence"]').text()).toBe('日落前 30 分钟')
      // 继承状态说明必须存在（节点级权威 + 规则级覆盖，O-3）
      expect(wrapper.find('[data-test="sun-location-state"]').text()).toContain('继承节点')
    })

    it('提交时映射为后端契约 trigger_type=sun_event + trigger_sun_kind（下拉代理值不得泄漏）', async () => {
      mockedAutomationApi.createRule.mockResolvedValue({ ...ruleFixture, id: 9 })
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="field-name"]').setValue('日落关断')
      await wrapper.find('[data-test="field-trigger-type"]').setValue('sunset')
      await flushPromises()
      await wrapper.find('[data-test="field-sun-offset"]').setValue('-30')
      await wrapper.find('[data-test="action-device-0"]').setValue('10')
      await wrapper.find('[data-test="action-id-0"]').setValue('read_basic_info')
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()

      const payload = mockedAutomationApi.createRule.mock.calls[0][0] as unknown as Record<string, unknown>
      expect(payload.trigger_type).toBe('sun_event')
      expect(payload.trigger_sun_kind).toBe('sunset')
      expect(payload.trigger_sun_offset_min).toBe(-30)
      // 坐标留空 ⇒ 省略（继承节点），不得发空串/null
      expect(payload).not.toHaveProperty('latitude')
      expect(payload).not.toHaveProperty('timezone')
    })
  })

  describe('F-2 多动作卡片', () => {
    it('渲染 N 张卡片，可增、可删、可上移，且提交的 seq 随顺序重排', async () => {
      mockedAutomationApi.createRule.mockResolvedValue({ ...ruleFixture, id: 11 })
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="field-name"]').setValue('多动作')
      await wrapper.find('[data-test="field-trigger-device"]').setValue('10')
      await wrapper.find('[data-test="field-sensor"]').setValue('temperature')

      // 初始 1 张 → 添加两张 = 3 张
      expect(wrapper.findAll('[data-test="action-card"]')).toHaveLength(1)
      await wrapper.find('[data-test="add-action"]').trigger('click')
      await wrapper.find('[data-test="add-action"]').trigger('click')
      expect(wrapper.findAll('[data-test="action-card"]')).toHaveLength(3)

      // 填三个动作（第 1 个用普通动作，避免触发风险清单）
      await wrapper.find('[data-test="action-device-0"]').setValue('10')
      await wrapper.find('[data-test="action-id-0"]').setValue('read_basic_info')
      await wrapper.find('[data-test="action-device-1"]').setValue('10')
      await wrapper.find('[data-test="action-id-1"]').setValue('act_b')
      await wrapper.find('[data-test="action-device-2"]').setValue('10')
      await wrapper.find('[data-test="action-id-2"]').setValue('act_c')

      // 首行「上移」disabled；第 3 张上移后成为第 2 张
      const ups = wrapper.findAll('[data-test="move-up"]')
      expect(ups[0].attributes('disabled')).toBeDefined()
      await ups[2].trigger('click')
      await flushPromises()

      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()
      const payload = mockedAutomationApi.createRule.mock.calls[0][0] as { actions: Array<{ seq: number; action_id: string }> }
      expect(payload.actions.map(a => a.seq)).toEqual([1, 2, 3])
      expect(payload.actions.map(a => a.action_id)).toEqual(['read_basic_info', 'act_c', 'act_b'])
    })

    it('删除按钮减少卡片；最后一张删除被拦（后端 actions 不能为空）', async () => {
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="add-action"]').trigger('click')
      expect(wrapper.findAll('[data-test="action-card"]')).toHaveLength(2)
      await wrapper.findAll('[data-test="remove-action"]')[1].trigger('click')
      await flushPromises()
      expect(wrapper.findAll('[data-test="action-card"]')).toHaveLength(1)

      const { ElMessage } = await import('element-plus')
      await wrapper.findAll('[data-test="remove-action"]')[0].trigger('click')
      await flushPromises()
      expect(wrapper.findAll('[data-test="action-card"]')).toHaveLength(1)
      expect(ElMessage.warning).toHaveBeenCalledWith('规则至少需要一个动作')
    })
  })

  describe('F-3 风险徽标与原因', () => {
    it('风险动作卡片渲染「图标 + 文字」徽标与风险原因块（非仅靠颜色）', async () => {
      mockedAutomationApi.listRules.mockResolvedValue([{
        ...ruleFixture,
        actions: [{ seq: 1, action_type: 'device_action', action_device_id: 10, action_id: 'set_mos_policy' }],
      }])
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="action-device-0"]').setValue('10')
      await wrapper.find('[data-test="action-id-0"]').setValue('set_mos_policy')
      await flushPromises()

      const badge = wrapper.find('[data-test="risk-badge"]')
      expect(badge.exists()).toBe(true)
      // 非仅靠颜色：文字 + 图标都要在
      expect(badge.text()).toContain('风险')
      expect(badge.text()).toContain('⚠')
      expect(wrapper.find('[data-test="risk-reason"]').exists()).toBe(true)
      // 卡片本身也带风险态（第四重编码）
      expect(wrapper.find('[data-test="action-card"]').attributes('data-risk')).toBe('1')
    })

    it('动作不在目录里时显示「等级未知」而不是「普通」（不得替后端断言不危险）', async () => {
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="action-device-0"]').setValue('10')
      await wrapper.find('[data-test="action-id-0"]').setValue('some_unlisted_action')
      await flushPromises()

      expect(wrapper.find('[data-test="unknown-badge"]').text()).toContain('等级未知')
      expect(wrapper.find('[data-test="normal-badge"]').exists()).toBe(false)
      expect(wrapper.find('[data-test="risk-badge"]').exists()).toBe(false)
    })

    it('普通动作卡片渲染「普通」徽标、无风险原因块、无红边（F-6）', async () => {
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="action-device-0"]').setValue('10')
      await wrapper.find('[data-test="action-id-0"]').setValue('read_basic_info')
      await flushPromises()

      expect(wrapper.find('[data-test="normal-badge"]').text()).toContain('普通')
      expect(wrapper.find('[data-test="risk-badge"]').exists()).toBe(false)
      expect(wrapper.find('[data-test="risk-reason"]').exists()).toBe(false)
      expect(wrapper.find('[data-test="action-card"]').attributes('data-risk')).toBe('0')
    })
  })

  describe('F-4 保存前风险清单弹窗（O-2 授权落地）', () => {
    it('含风险动作时弹清单，列出全部风险动作，未勾满时「保存规则」disabled', async () => {
      mockedAutomationApi.precheckRule.mockResolvedValue(riskPrecheck())
      const wrapper = await mountPage()
      await openCreateFilled(wrapper, '日落关断 BMS')
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()

      expect(wrapper.find('[data-test="save-risk-dialog"]').exists()).toBe(true)
      expect(wrapper.findAll('[data-test="risk-item"]')).toHaveLength(1)
      const item = wrapper.find('[data-test="risk-item"]')
      // 清单必须列全 动作名 + 目标设备 + 动作 ID + 参数 + 风险原因
      expect(item.text()).toContain('设置充放电 MOS 软件策略')
      expect(item.text()).toContain('BMS-01')
      expect(item.text()).toContain('set_mos_policy')
      expect(item.text()).toContain('discharge_software_closed=true')
      expect(item.text()).toContain('关闭充放电 MOS')

      const confirmBtn = wrapper.find('[data-test="confirm-save"]')
      expect(confirmBtn.attributes('disabled')).toBeDefined()
      expect(wrapper.find('[data-test="ack-progress"]').text()).toContain('已确认 0 / 1')

      // 勾选后才解禁
      await wrapper.find('[data-test="risk-ack-1"]').setValue(true)
      await flushPromises()
      expect(wrapper.find('[data-test="confirm-save"]').attributes('disabled')).toBeUndefined()
      expect(wrapper.find('[data-test="ack-progress"]').text()).toContain('已确认 1 / 1')
      // 未点「保存规则」前不得落库
      expect(mockedAutomationApi.createRule).not.toHaveBeenCalled()
    })

    it('勾选后点「保存规则」提交 risk_acknowledged=true（O-2 规则级授权）', async () => {
      mockedAutomationApi.precheckRule.mockResolvedValue(riskPrecheck())
      mockedAutomationApi.createRule.mockResolvedValue({ ...ruleFixture, id: 12 })
      const wrapper = await mountPage()
      await openCreateFilled(wrapper, '日落关断 BMS')
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()
      await wrapper.find('[data-test="risk-ack-1"]').setValue(true)
      await flushPromises()
      await wrapper.find('[data-test="confirm-save"]').trigger('click')
      await flushPromises()

      expect(mockedAutomationApi.createRule).toHaveBeenCalledTimes(1)
      expect(mockedAutomationApi.createRule.mock.calls[0][0]).toEqual(
        expect.objectContaining({ risk_acknowledged: true }),
      )
      expect(wrapper.find('[data-test="save-risk-dialog"]').exists()).toBe(false)
    })

    it('未勾选时「保存规则」不可用，且直接点它也不落库（不靠 UI disabled 兜底）', async () => {
      mockedAutomationApi.precheckRule.mockResolvedValue(riskPrecheck())
      const wrapper = await mountPage()
      await openCreateFilled(wrapper, '日落关断 BMS')
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()

      // ① UI 层：按钮必须 disabled。
      expect(wrapper.find('[data-test="confirm-save"]').attributes('disabled')).toBeDefined()
      // ② 函数层：**直接调用处理函数**绕过 DOM 的 disabled 短路。
      //    为什么必须这样测：VTU 的 trigger('click') 对 disabled 元素直接跳过派发
      //    （实测：只断言"点 disabled 按钮不落库"时，把函数内的 allRisksAcked 判定
      //    删掉本用例仍然全绿 —— 门禁变成恒真）。disabled 只是 UI 属性，
      //    键盘/程序化调用/未来快捷键都可能绕过它，函数内必须再判一次。
      const vm = wrapper.vm as unknown as { confirmRiskSave: () => Promise<void> }
      await vm.confirmRiskSave()
      await flushPromises()
      expect(mockedAutomationApi.createRule).not.toHaveBeenCalled()
    })

    it('绕过弹窗直接再触发保存会被拦（onSave 入口护栏，不依赖按钮 disabled）', async () => {
      mockedAutomationApi.precheckRule.mockResolvedValue(riskPrecheck())
      const wrapper = await mountPage()
      await openCreateFilled(wrapper, '日落关断 BMS')
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-test="save-risk-dialog"]').exists()).toBe(true)
      expect(mockedAutomationApi.precheckRule).toHaveBeenCalledTimes(1)

      // 弹窗已开时再次点「保存」：必须在入口被拒。
      // 断言"没有第二次预检"而不只是"没有落库" —— 后者在护栏缺失时也成立
      // （第二次 onSave 会再弹一次清单，仍然不会 createRule，断言会假绿）。
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()
      expect(mockedAutomationApi.precheckRule).toHaveBeenCalledTimes(1)
      expect(mockedAutomationApi.createRule).not.toHaveBeenCalled()

      // 取消后不得落库
      await wrapper.find('[data-test="risk-cancel"]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-test="save-risk-dialog"]').exists()).toBe(false)
      expect(mockedAutomationApi.createRule).not.toHaveBeenCalled()
    })

    // ── 预检端点不可用时的降级（后端 precheck 尚未实现，必须能保存） ──────────
    //
    // 降级**不得**等于"跳过风险确认"：判定源换成后端已下发的动作目录
    // （GET /edge-devices/:id/actions 的 risk_level / requires_confirmation），
    // 仍然是后端字段、仍然没有第二份风险等级表。下面三条分别钉住三种结局。

    it('预检不可用 + 普通动作：降级判定为无风险动作，正常保存（不得把端点缺失变成"谁都不能存"）', async () => {
      mockedAutomationApi.precheckRule.mockResolvedValue(null)
      mockedAutomationApi.createRule.mockResolvedValue({ ...ruleFixture, id: 14 })
      const wrapper = await mountPage()
      await openCreateFilled(wrapper, '预检缺失但普通')
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()

      expect(wrapper.find('[data-test="save-risk-dialog"]').exists()).toBe(false)
      expect(mockedAutomationApi.createRule).toHaveBeenCalledTimes(1)
    })

    it('预检不可用 + 风险动作：降级仍弹风险清单（不得静默放行）', async () => {
      mockedAutomationApi.precheckRule.mockResolvedValue(null)
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="field-name"]').setValue('降级风险')
      await wrapper.find('[data-test="field-trigger-device"]').setValue('10')
      await wrapper.find('[data-test="field-sensor"]').setValue('temperature')
      await wrapper.find('[data-test="action-device-0"]').setValue('10')
      await wrapper.find('[data-test="action-id-0"]').setValue('set_mos_policy')
      await flushPromises()
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()

      expect(wrapper.find('[data-test="save-risk-dialog"]').exists()).toBe(true)
      expect(wrapper.findAll('[data-test="risk-item"]')).toHaveLength(1)
      // 降级路径的风险原因来自 Catalog description，且必须**显式标注它不是危险说明**
      // （后端未实现 risk_reason，编造安全文案比没有更危险）
      expect(wrapper.find('[data-test="risk-item"]').text()).toContain('非危险说明')
      expect(mockedAutomationApi.createRule).not.toHaveBeenCalled()
    })

    it('预检不可用 + 动作目录未加载：无法判定风险 ⇒ 阻止保存（不得把未知当普通）', async () => {
      mockedAutomationApi.precheckRule.mockResolvedValue(null)
      // 目录加载失败（模拟：接口报错）⇒ 索引里没有该动作，等级不可判定
      mockedOperationApi.actions.mockRejectedValue(new Error('catalog unavailable'))
      const wrapper = await mountPage()
      await wrapper.find('[data-test="create-rule"]').trigger('click')
      await wrapper.find('[data-test="field-name"]').setValue('目录缺失')
      await wrapper.find('[data-test="field-trigger-device"]').setValue('10')
      await wrapper.find('[data-test="field-sensor"]').setValue('temperature')
      await wrapper.find('[data-test="action-device-0"]').setValue('10')
      await wrapper.find('[data-test="action-id-0"]').setValue('set_mos_policy')
      await flushPromises()
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()

      expect(mockedAutomationApi.createRule).not.toHaveBeenCalled()
      expect(wrapper.find('[data-test="save-risk-dialog"]').exists()).toBe(false)
    })

    it('F-6 普通动作保存不弹二次确认，直接落库', async () => {
      // 默认预检无风险动作
      mockedAutomationApi.createRule.mockResolvedValue({ ...ruleFixture, id: 13 })
      const wrapper = await mountPage()
      await openCreateFilled(wrapper, '普通规则')
      await wrapper.find('[data-test="save-rule"]').trigger('click')
      await flushPromises()

      expect(wrapper.find('[data-test="save-risk-dialog"]').exists()).toBe(false)
      expect(mockedAutomationApi.createRule).toHaveBeenCalledTimes(1)
    })
  })

  describe('F-8 新结果码三处映射齐全', () => {
    // 三个新枚举：confirmation_required / partial_failure / suppressed_no_solar_event。
    // 只改一处（例如只加 resultOptions）时必须变红 —— 这是本门禁的靶点。
    const NEW_RESULTS = ['confirmation_required', 'partial_failure', 'suppressed_no_solar_event'] as const

    it('结果筛选下拉（resultOptions）含三个新枚举且都有中文标签', async () => {
      const wrapper = await mountPage()
      const labels = wrapper.find('[data-test="filter-result"]').findAll('option').map(o => o.text())
      for (const r of NEW_RESULTS) {
        // 未映射的枚举在 resultText 里会回落到英文原串 —— 断言"不得出现英文原串"
        expect(labels.join('|')).not.toContain(r)
      }
      expect(labels).toContain('需确认（未授权）')
      expect(labels).toContain('部分失败')
      expect(labels).toContain('无日出日落事件')
    })

    it('事件表（resultText + resultTagType）对三个新枚举都有中文与标签色', async () => {
      mockedAutomationApi.listEvents.mockResolvedValue(eventPage(
        NEW_RESULTS.map((result, i) => ({ ...eventFixture, id: 200 + i, result })),
      ))
      const wrapper = await mountWithSlotTable()
      const rows = wrapper.findAll('[data-test="events-table"] tbody tr')
      expect(rows).toHaveLength(3)
      const texts = rows.map(r => r.text())
      expect(texts[0]).toContain('需确认（未授权）')
      expect(texts[1]).toContain('部分失败')
      expect(texts[2]).toContain('无日出日落事件')
      // 三处映射齐全 ⇒ 表格里不得出现英文原串
      expect(texts.join('|')).not.toMatch(/confirmation_required|partial_failure|suppressed_no_solar_event/)
    })
  })

  describe('F-9 订阅 automation_confirmation_required（C5 前端闭环）', () => {
    it('订阅表含新事件名，且收到推送时给出用户可见提示（不只写事件表）', async () => {
      const { useWebSocketStore } = await import('@/stores/websocket')
      const pinia = createPinia()
      const store = useWebSocketStore(pinia)
      const captured = new Map<string, (m: unknown) => void>()
      vi.spyOn(store, 'subscribe').mockImplementation(((type: string, handler: (m: never) => void) => {
        captured.set(type, handler as (m: unknown) => void)
        return () => { captured.delete(type) }
      }) as typeof store.subscribe)
      mount(AutomationRules, { global: { plugins: [pinia] } })
      await flushPromises()

      expect([...captured.keys()]).toContain('automation_confirmation_required')
      expect([...captured.keys()]).toContain('automation_partial_failure')

      const { ElMessage } = await import('element-plus')
      // 代码走的是 ElMessage.warning(...)（不是 ElMessage({...})）—— 断言必须落在
      // 真正被调用的那个 mock 上，否则会拿到空 calls 而"看起来"没有提示。
      const warnMock = (ElMessage as unknown as { warning: { mock: { calls: unknown[][] } } }).warning
      captured.get('automation_confirmation_required')!({
        type: 'automation_confirmation_required',
        payload: { detail: { rule_name: '日落关断 BMS', action_id: 'set_mos_policy' } },
      })
      await flushPromises()
      // 用户可见提示必须点名规则与动作（C5 的缺陷形态就是"前端毫无提示"）
      expect(warnMock.mock.calls.length).toBeGreaterThan(0)
      const last = JSON.stringify(warnMock.mock.calls.at(-1))
      expect(last).toContain('日落关断 BMS')
      expect(last).toContain('set_mos_policy')
      vi.restoreAllMocks()
    })
  })

  describe('F-10 窄屏 ≤480px 断点', () => {
    it('样式含 480px 断点，且动作卡操作区与弹窗 footer 在该断点下换行/撑满', async () => {
      const css = await compiledScopedCss(source, 'AutomationRules.vue')
      // 按**花括号配对**取媒体块，而不是 \n} 这种格式假设 —— 编译产物的缩进/换行
      // 不是契约，用格式去匹配会让门禁在无关的格式变化下假红。
      function mediaBlock(maxWidth: number): string | null {
        // 编译产物是 `@media (max-width: 480px)`（@media 与括号之间有空格），
        // 故必须允许 \s*；写死 "@media(" 会让门禁永远找不到该断点（假红）。
        const start = css.search(new RegExp('@media\\s*\\(max-width:\\s*' + maxWidth + 'px\\)'))
        if (start < 0) return null
        const open = css.indexOf('{', start)
        if (open < 0) return null
        let depth = 0
        for (let i = open; i < css.length; i++) {
          if (css[i] === '{') depth++
          else if (css[i] === '}') {
            depth--
            if (depth === 0) return css.slice(open + 1, i)
          }
        }
        return null
      }

      const body = mediaBlock(480)
      expect(body, '缺少 ≤480px 断点：窄屏下多动作卡片与弹窗布局没有兜底').not.toBeNull()
      // 动作卡操作区换行 + 弹窗 footer 撑满（设计稿 §7 的 480px 档）
      expect(body!).toContain('.action-card__ops')
      expect(body!).toContain('.el-dialog__footer')
      // 弹窗变底部抽屉：满宽
      expect(body!).toContain('width: 100%')
      // 触控目标补足（theme.css 的 44px 口径）
      expect(body!).toContain('min-height: 44px')
      // 768px 主断点也必须存在（元数据单列）
      const wide = mediaBlock(768)
      expect(wide).not.toBeNull()
      expect(wide!).toContain('.action-meta')
    })
  })

  // ── C-ops 审计回归钉（2026-10-21）────────────────────────────────────────
  describe('C-ops 空列表 / 触发历史卡片头 / 固定列契约', () => {
    it('total=0 时不渲染分页器；「刷新」入口仍在（空态不是"功能消失"）', async () => {
      mockedAutomationApi.listEvents.mockResolvedValue(eventPage([]))
      const wrapper = await mountPage()
      expect(wrapper.find('.events-pagination').exists(), '空列表仍渲染分页器（共 0 条）').toBe(false)
      expect(wrapper.find('[data-test="refresh-events"]').exists()).toBe(true)
    })

    it('触发历史卡片头可换行、标题单行不压缩、筛选条自身可换行（编译产物级）', async () => {
      const css = await compiledScopedCss(source, 'AutomationRules.vue')
      expect(css, '.card-head 缺 flex-wrap：标题会被筛选条挤成逐字竖排').toMatch(/\.card-head\[data-v-[a-z0-9]+\] \{[^}]*flex-wrap: wrap/)
      expect(css, '.card-title 缺 white-space: nowrap').toMatch(/\.card-title\[data-v-[a-z0-9]+\] \{[^}]*white-space: nowrap/)
      expect(css, '.event-filters 缺 flex-wrap：筛选条会溢出卡片').toMatch(/\.event-filters\[data-v-[a-z0-9]+\] \{[^}]*flex-wrap: wrap/)
    })

    it('触发历史表「操作」列保持 fixed=right（F26 判定 100px 不遮挡，不得顺手改）', () => {
      // 2026-10-21 复审裁决：C-ops 审计把本列标为「待确认 P2」（当时无数据、只看到白色条带），
      // 但 F26 契约（src/views/__tests__/FixedColumnOcclusion.spec.ts）明文裁决：
      //   「事件表操作列 100px，fixedW 100 < 表格盒 280/310，该列唯一控件中心始终落在粘性区之外，
      //    48/48 命中自身 ⇒ 保持 fixed」——改动它会触发 F26 的「超出修复范围」反证断言。
      // 因此这里锁定「保持无条件 fixed」，防止后人再按猜测改成响应式绑定。
      expect(source).toMatch(/label="操作" width="100" fixed="right"/)
      expect(source).not.toMatch(/label="操作" width="100" :fixed="isMobile/)
    })

    it('「条件」阈值框盒宽 ≥176px（内层 input ≥84px），不再横向溢出弹窗', () => {
      expect(source).toMatch(/\.cond-row \.el-input-number \{[^}]*min-width: 176px/)
      expect(source).toMatch(/\.cond-row \{[^}]*flex-wrap: wrap/)
    })
  })

  // ── 2026-10-21 第二轮截图精读新增回归钉（/automation 与 /alerts 审计）──────
  describe('第二轮审计：底部抽屉贴底 / 窄屏长文本不截断', () => {
    it('≤480 底部抽屉真正贴底：overlay 容器改 flex + align-items:flex-end', async () => {
      // 实测（393）：只归零 .el-dialog 的 margin 时，外层 .el-overlay-dialog 是
      // display:block ⇒ 92vh 弹窗顶对齐，dialog y=0/bottom=783，底部留 68px 灰带、
      // 顶部圆角出现在屏幕最上方 —— 不是"底部抽屉"。必须把容器本身改成 flex 底部对齐。
      const css = await compiledScopedCss(source, 'AutomationRules.vue')
      expect(css, '缺 .el-overlay-dialog 的 flex 底部对齐：底部抽屉会顶对齐').toMatch(
        /\.el-overlay-dialog[^{]*\{[^}]*display:\s*flex/,
      )
      // 反向护栏：把 align-items 声明删掉后同一正则必须匹配不到（证明它测的是真实声明）
      expect(css).toMatch(/\.el-overlay-dialog[^{]*\{[^}]*align-items:\s*flex-end/)
      expect(css.replace(/align-items:\s*flex-end/g, '')).not.toMatch(
        /\.el-overlay-dialog[^{]*\{[^}]*align-items:\s*flex-end/,
      )
    })

    it('窄屏卡片列表：完整名称 + 桌面各列信息 + 行内控件全部保留（不拿信息/功能换整洁）', async () => {
      viewportWidth.value = 393
      isMobileRef.value = true
      const wrapper = await mountPage()
      const cards = wrapper.findAll('[data-test="rule-card"]')
      expect(cards.length, '窄屏缺少规则卡片').toBe(1)
      const txt = cards[0].text()
      expect(txt, '被修掉的正是「高温…」省略号截断').toContain('高温开窗')
      expect(txt, '触发器类型徽标').toContain('传感器阈值')
      expect(txt, '触发条件摘要').toContain('temperature > 30')
      expect(txt, '冷却/熔断').toContain('300s / 10')
      expect(txt, '确认制').toContain('需确认')
      expect(cards[0].findAll('[data-test="rule-enabled"]').length, '启用开关').toBe(1)
      expect(cards[0].findAll('[data-test="trigger-rule"]').length, '触发按钮').toBe(1)
    })

    it('窄屏事件卡片：时间/规则名/结果/触发值/详情/待确认操作齐全', async () => {
      viewportWidth.value = 393
      isMobileRef.value = true
      const wrapper = await mountPage()
      const cards = wrapper.findAll('[data-test="event-card"]')
      expect(cards.length, '窄屏缺少事件卡片').toBe(1)
      const txt = cards[0].text()
      expect(txt).toContain('高温开窗')
      expect(txt).toContain('待确认')
      expect(txt).toContain('31.5')
      expect(txt).toContain('高温触发，等待确认')
      expect(cards[0].findAll('[data-test="confirm-event"]').length, '待确认事件的「确认执行」入口').toBe(1)
    })

    it('桌面：仍渲染表格、不渲染卡片列表（>768px 结构不变）', async () => {
      const wrapper = await mountPage()
      expect(wrapper.find('[data-test="rules-table"]').exists()).toBe(true)
      expect(wrapper.find('[data-test="events-table"]').exists()).toBe(true)
      expect(wrapper.find('[data-test="rules-cards"]').exists()).toBe(false)
      expect(wrapper.find('[data-test="events-cards"]').exists()).toBe(false)
    })

    it('源码契约：两处表格包在 v-if="!isMobile" 里，卡片列表是 v-else 分支（否则窄屏会同时出现表格+卡片）', () => {
      expect(source.match(/v-if="!isMobile" class="mobile-table-wrapper"/g)?.length).toBe(2)
      expect(source).toMatch(/v-else class="rule-cards"/)
      expect(source).toMatch(/v-else class="event-cards"/)
      expect(source).toContain('data-test="rules-cards"')
      expect(source).toContain('data-test="events-cards"')
    })
  })
})

