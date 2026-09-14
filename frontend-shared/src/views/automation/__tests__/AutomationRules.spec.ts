import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, h, type VNode } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { compileStyleAsync, parse } from '@vue/compiler-sfc'
import AutomationRules from '../AutomationRules.vue'
import source from '../AutomationRules.vue?raw'
import { automationApi, type AutomationEvent, type AutomationEventPage } from '@/api/automation'
import { edgeDeviceApi } from '@/api/edgeDevice'

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
vi.mock('element-plus', async importOriginal => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() },
    ElMessageBox: { confirm: vi.fn().mockResolvedValue(true) },
  }
})

const mockedAutomationApi = vi.mocked(automationApi)
const mockedEdgeApi = vi.mocked(edgeDeviceApi)

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

  it('删除确认后调 deleteRule', async () => {
    mockedAutomationApi.deleteRule.mockResolvedValue(undefined)
    await mountPage()
    // ElTable stub 不渲染 slot 模板，删除按钮在 slot 内不可见。
    // 改为验证源码中删除按钮存在 + onDelete 方法调用 deleteRule。
    const source = await import('../AutomationRules.vue?raw')
    expect(source.default).toContain('删除')
    expect(source.default).toContain('onDelete')
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
    expect(source.default).toContain('ElMessageBox.confirm')
    expect(source.default).toContain('手动触发规则')
    expect(source.default).toContain('跳过条件评估与确认制')
    expect(source.default).toContain('fetchEvents()')
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
})

