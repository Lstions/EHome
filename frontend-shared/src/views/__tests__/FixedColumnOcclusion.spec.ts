import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { parse } from '@vue/compiler-sfc'

/**
 * F26 合同：窄屏下操作列不得是「粘性固定列」（规范 §4.3.2.2 / §4.2.3）。
 *
 * ## 守护的真实缺陷（主控 2026-09-14 独立复现；本轮复核把范围修正为**两处**）
 *
 * el-table 的 \`fixed="right"\` 在当前 EP 版本渲染为 \`position:sticky\`，绘制顺序恒在
 * 普通列之上。当固定列宽接近/超过表格盒宽时，它会把相邻数据列的**可交互控件整列盖住**
 * ——48 点 \`elementFromPoint\` 网格实测：
 *
 * | 路由 | 视口 | 固定列/表格盒 | 占比 | 被遮挡控件 | 开关命中自身 |
 * |---|---|---|---|---|---|
 * | /automation 规则表 | 390px | 180/310 | 58.1% | 「启用」开关 ×2 | **0/48**（另 48/48 命中固定列） |
 * | /automation 规则表 | 1440px | 180/1160 | 15.5% | 0 | 48/48（桌面正常） |
 * | /data-sources | 390px | 330/310 | **106.5%** | 「名称」链接等 3 个 | **0/48** |
 * | /data-sources | 360px | 330/280 | **117.9%** | 同上 3 个 | **0/48** |
 *
 * 主控的扫描器只报了 /automation：它只统计「固定列 + 非固定列控件」的**非空**组合，
 * 而 /data-sources 在 ehome_uiux 里是空表 ⇒ 控件分母为 0（契约 §2.3 的样本缺失）。
 * 本轮用 \`page.route\` 给该表注入 3 行后实测**确有 3 个被遮挡控件**，两处都已修。
 *
 * ## 裁决：窄屏取消固定（列宽与内容一字不改），不是收窄操作列
 *
 * 先按 EdgeDeviceList.vue 范式把 /data-sources 的 330px 收窄成 96px 图标按钮，
 * **被真实产物实测否决**：5 个 36px 热区放不进 96px，折成 5 行，桌面行高 40px → 183px
 * （直接改变桌面视觉密度，违反 §4.2.3 与本任务硬性约束）；收窄到单行所需的 208px
 * 仍在窄屏遮挡（67.1%）。故两页统一：\`<el-table-column :fixed="isMobile ? false : 'right'">\`。
 * 桌面（>=769px）渲染路径与改前**逐像素等价**：实测 fixed 列宽 330/180、墨宽 258/132、
 * 按钮行数 1、行高 40/49 全部与改前一致。
 *
 * ## 度量原语与可校准性（happy-dom 无布局，故这里测「语义」而不是像素）
 *
 * happy-dom 没有布局引擎，测不出 elementFromPoint 命中率 —— 像素级证据在真浏览器探针
 * \`.tmp-probe/f26-acceptance.mjs\`（改前 390px 遮挡 5 处 → 改后 0 处，md5 逐字节可复现）。
 * 本文件守护的是**可机械判定的语义事实**：两页在窄屏真的把 \`fixed\` 传成了 false、
 * 在桌面真的仍然传 'right'、且列宽/内容没有被顺手改掉（防止「假修」：删掉 fixed 或
 * 把宽列改成窄列都会改变桌面密度，必须被本文件挡住）。
 *
 * 度量原语 = VTU 的 \`findAllComponents({ name: 'ElTableColumn' })\`：直接读组件**实际收到的
 * props**，不是源码字符串包含判断。下面反证组证明它不是恒真。
 */

// ── 页面依赖的 API mock（与两页既有 spec 同法，不依赖 ehome_uiux 数据）────────
vi.mock('@/api/automation', () => ({
  automationApi: {
    listRules: vi.fn(), createRule: vi.fn(), updateRule: vi.fn(), removeRule: vi.fn(),
    setRuleEnabled: vi.fn(), listEvents: vi.fn(), confirmEvent: vi.fn(), triggerRule: vi.fn(),
  },
}))
vi.mock('@/api/dataSource', () => ({
  dataSourceApi: {
    list: vi.fn(), get: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn(),
    activate: vi.fn(), deactivate: vi.fn(), reset: vi.fn(), getHealth: vi.fn(), getFailoverLogs: vi.fn(),
  },
}))
vi.mock('@/api/logicalDevice', () => ({ logicalDeviceApi: { list: vi.fn() } }))
vi.mock('@/api/edgeDevice', () => ({ edgeDeviceApi: { getList: vi.fn() } }))

/**
 * 视口宽度：两页都用 \`@/composables/useResponsive\`（模块级共享 ref），直接驱动它
 * 模拟窄屏/桌面，无需真实 resize。isMobile 必须是**真 computed** —— 普通对象在模板里
 * 恒为 truthy，会让「仅窄屏取消固定」的断言在桌面也成立（假绿）或反之。
 */
const { widthRef, isMobileRef } = vi.hoisted(() => {
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  const { ref, computed } = require('vue') as typeof import('vue')
  const w = ref(1440)
  return { widthRef: w, isMobileRef: computed(() => w.value < 768) }
})
vi.mock('@/composables/useResponsive', () => ({
  BREAKPOINTS: { xs: 480, sm: 640, md: 768, lg: 1024, xl: 1280, '2xl': 1536 },
  useResponsive: () => ({
    width: widthRef,
    isMobile: isMobileRef,
    isTablet: { value: false },
    isDesktop: { value: widthRef.value >= 1024 },
  }),
}))

vi.mock('element-plus', async (importOriginal) => {
  const actual = await importOriginal<typeof import('element-plus')>()
  return {
    ...actual,
    ElMessage: Object.assign(vi.fn(), { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() }),
    ElMessageBox: { confirm: vi.fn().mockResolvedValue(true) },
  }
})

const FIXTURE_RULE = {
  id: 1, name: '高温开窗', enabled: true, trigger_type: 'sensor_threshold',
  trigger_edge_device_id: 1, trigger_sensor_name: 'temperature', trigger_comparator: 'gt',
  trigger_threshold: 40, trigger_duration_sec: 0, trigger_window_start: '', trigger_window_end: '',
  trigger_window_edge: 'enter', conditions_json: '[]', action_type: 'device_action',
  action_device_id: 1, action_id: 'gpio_set', action_params_json: '{}', action_level: 'info',
  require_confirmed: false, cooldown_sec: 60, max_daily_exec: 100,
  created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z',
}
const FIXTURE_SOURCE = {
  id: 1, device_id: 10, category: 'temperature', edge_device_id: 100, source_type: 'edge_device',
  name: '温度主来源', description: '', priority: 10, is_primary: true, max_fail_count: 3,
  fail_count: 0, status: 'active', last_success: '2026-09-01T00:00:00Z', last_failure: null,
  config: '', created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z',
}

/**
 * 列探针：只声明本 spec 关心的 props（label/width/fixed），并声明 \`name: 'ElTableColumn'\`
 * 使 VTU 能按名字取到组件实例。**不渲染 DOM** —— 列组件在 EP 里是抽象组件，
 * 断言 DOM 元素是不可靠的（第一版就栽在这里：\`findAll('.el-table-column')\` 拿到 0 个，
 * 分母守卫正确地拒绝了假绿）。这里改为直接读组件实例的 props。
 */
const ColumnProbe = defineComponent({
  name: 'ElTableColumn',
  props: {
    prop: String,
    label: String,
    width: [String, Number],
    minWidth: [String, Number],
    fixed: { type: [String, Boolean], default: undefined },
    type: String,
  },
  setup() {
    return () => null
  },
})

/** 表格替身：只做一件事 —— 渲染默认插槽，使列组件真正被挂载（否则列实例为 0）。 */
const TableStub = defineComponent({
  name: 'ElTable',
  props: { data: { type: Array, default: () => [] } },
  setup(_, { slots }) {
    return () => h('table', { class: 'el-table' }, slots.default ? [slots.default()] : [])
  },
})

async function mountWithProbe(component: unknown): Promise<VueWrapper> {
  const wrapper = mount(component as never, {
    global: {
      plugins: [createPinia()],
      components: {
        ElTable: TableStub, 'el-table': TableStub,
        ElTableColumn: ColumnProbe, 'el-table-column': ColumnProbe,
      },
    },
  })
  await flushPromises()
  return wrapper as VueWrapper
}

/**
 * 取指定列宽的「操作」列实测 props。**按列宽定位是刻意的**：
 * /automation 有两张表，规则表操作列（180px，已改响应式）与事件表操作列
 * （100px，保持 fixed —— 主控与本轮探针都实测它不遮挡：fixedW 100 < 表格盒 280/310，
 * 且该列唯一控件「确认执行」的中心始终落在粘性区之外，48/48 命中自身）。
 * 只按 label 取会把两张表混在一起，断言会指向错误的列。
 */
function operationColumns(wrapper: VueWrapper, width: string): Array<{ fixed: unknown; width: unknown }> {
  const all = wrapper.findAllComponents(ColumnProbe)
  expect(
    all.length,
    '没有挂载到任何 el-table-column 实例：度量原语失效（分母为 0），' +
      '此时「找不到固定列」不能作为合规证据'
  ).toBeGreaterThan(0)
  const cols = all.filter((c) => c.props('label') === '操作' && String(c.props('width')) === width)
  expect(
    cols.length,
    '没有找到 width=' + width + ' 的「操作」列（共 ' + all.length + ' 列，其中「操作」列 ' +
      all.filter((c) => c.props('label') === '操作').length + ' 个）：分母为 0，断言会假绿'
  ).toBeGreaterThan(0)
  return cols.map((c) => ({ fixed: c.props('fixed'), width: String(c.props('width')) }))
}

/** 绿灯判据的唯一入口：窄屏必须「不固定」。反证组用同一个函数证明它会返回 false。 */
function isUnfixed(fixed: unknown): boolean {
  return fixed === false
}

const SRC: Record<string, string> = {
  'AutomationRules.vue': readFileSync(resolve(process.cwd(), 'src/views/automation/AutomationRules.vue'), 'utf8'),
  'DataSourceList.vue': readFileSync(resolve(process.cwd(), 'src/views/data-source/DataSourceList.vue'), 'utf8'),
}

// 让两个页面各自的数据接口返回确定数据（不依赖 ehome_uiux）
async function primeMocks(kind: 'automation' | 'datasource') {
  if (kind === 'automation') {
    const { automationApi } = await import('@/api/automation')
    vi.mocked(automationApi.listRules).mockResolvedValue([FIXTURE_RULE as never])
    vi.mocked(automationApi.listEvents).mockResolvedValue({ items: [], total: 0 } as never)
    const { edgeDeviceApi } = await import('@/api/edgeDevice')
    vi.mocked(edgeDeviceApi.getList).mockResolvedValue({ items: [] } as never)
  } else {
    const { dataSourceApi } = await import('@/api/dataSource')
    vi.mocked(dataSourceApi.list).mockResolvedValue({ items: [FIXTURE_SOURCE as never], total: 1 } as never)
    const { logicalDeviceApi } = await import('@/api/logicalDevice')
    vi.mocked(logicalDeviceApi.list).mockResolvedValue({ items: [], total: 0 } as never)
  }
}

async function mountPage(kind: 'automation' | 'datasource', width: number) {
  await primeMocks(kind)
  widthRef.value = width
  const mod = kind === 'automation'
    ? await import('@/views/automation/AutomationRules.vue')
    : await import('@/views/data-source/DataSourceList.vue')
  return mountWithProbe(mod.default)
}

describe('F26 窄屏固定列不得遮挡数据列 — 操作列固定语义', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    widthRef.value = 1440
  })

  it('automation @1440px：操作列仍 fixed="right" 且宽 180px（桌面密度不变）', async () => {
    const wrapper = await mountPage('automation', 1440)
    const cols = operationColumns(wrapper, '180')
    expect(
      cols.map((c) => c.fixed),
      '桌面端操作列必须仍然 fixed="right"：窄屏修复不得改变宽屏的固定列语义'
    ).toEqual(cols.map(() => 'right'))
    expect(
      cols.map((c) => c.width),
      '桌面端操作列宽必须保持 180px（§4.2.3 紧凑运维密度不得变化）'
    ).toEqual(cols.map(() => '180'))
  })

  it('automation @390px：操作列必须取消 fixed —— F26 的直接修复点', async () => {
    const wrapper = await mountPage('automation', 390)
    for (const c of operationColumns(wrapper, '180')) {
      expect(
        isUnfixed(c.fixed),
        '窄屏操作列仍是固定列（fixed=' + String(c.fixed) + '）：粘性列绘制顺序恒在普通列之上，' +
          '会整列盖住行内「启用」开关 —— 真浏览器实测 elementFromPoint 自身 0/48、命中固定列 48/48'
      ).toBe(true)
      expect(c.width, '窄屏只是取消固定，列宽不得变化（否则会改变桌面密度）').toBe('180')
    }
    // 对照：事件表操作列（100px）保持固定，且它经实测不遮挡（fixedW 100 << 表格盒 280/310）
    for (const c of operationColumns(wrapper, '100')) {
      expect(c.fixed, '事件表操作列在窄屏仍应保持 fixed="right"（实测不遮挡，无需改动）').toBe('right')
    }
  })

  it('data-sources @1440px：操作列仍 fixed="right" 且宽 330px', async () => {
    const wrapper = await mountPage('datasource', 1440)
    const cols = operationColumns(wrapper, '330')
    expect(cols.map((c) => c.fixed), '桌面端操作列必须仍然 fixed="right"').toEqual(cols.map(() => 'right'))
    expect(
      cols.map((c) => c.width),
      '桌面端操作列宽必须保持 330px：收窄会改变桌面密度（96px 实测折 5 行、行高 40→183px，已否决）'
    ).toEqual(cols.map(() => '330'))
  })

  it('data-sources @390px：操作列必须取消 fixed（330px > 表格盒 310px，占比 106.5%）', async () => {
    const wrapper = await mountPage('datasource', 390)
    for (const c of operationColumns(wrapper, '330')) {
      expect(
        isUnfixed(c.fixed),
        '窄屏操作列仍是固定列（fixed=' + String(c.fixed) + '）：330px 宽于表格盒 310px，' +
          '会把「名称」链接与行内操作按钮整列盖住（实测 0/48 命中自身）'
      ).toBe(true)
      expect(c.width, '窄屏只是取消固定，列宽不得变化').toBe('330')
    }
  })

  // ── 反证组：证明上面的判定器能变红（不是恒真）────────────────────────────

  it('反证①：把 fixed 写死成 "right" 的列，用同一判据必须判为「未修复」', async () => {
    const HardFixed = defineComponent({
      name: 'ElTableColumn',
      props: { label: String, width: [String, Number], fixed: { type: [String, Boolean], default: undefined } },
      setup() {
        return () => null
      },
    })
    const Harness = defineComponent({
      name: 'Harness',
      setup() {
        return () => h(TableStub, null, {
          default: () => [h(HardFixed, { label: '操作', width: 180, fixed: 'right' })],
        })
      },
    })
    const wrapper = mount(Harness, {
      global: { components: { ElTable: TableStub, ElTableColumn: HardFixed } },
    })
    await flushPromises()

    const all = wrapper.findAllComponents(HardFixed)
    expect(all.length, '反证组的分母也必须非 0（否则它证明不了任何事）').toBeGreaterThan(0)

    const fixed = all[0].props('fixed')
    expect(fixed, '硬写 fixed="right" 的列必须被读成 "right"').toBe('right')
    // 与绿灯用例**完全相同**的判据：这里必须为 false，否则说明 isUnfixed 是恒真的
    // （那 4 条绿灯用例就什么都没证明 —— 本仓「伪装成正常」的第 7 例）
    expect(
      isUnfixed(fixed),
      'isUnfixed 对写死 fixed 的列也返回 true —— 判定器恒真，绿灯用例失去意义'
    ).toBe(false)
  })

  it('反证②：两页模板确实用响应式绑定，且未顺手改列宽（防止「删掉 fixed」式假修）', () => {
    for (const [name, src] of Object.entries(SRC)) {
      const { descriptor, errors } = parse(src, { filename: name })
      expect(errors, name + ' SFC 解析失败: ' + JSON.stringify(errors)).toEqual([])
      const tpl = descriptor.template?.content ?? ''
      const opCols = tpl.match(/<el-table-column[^>]*label="操作"[^>]*>/g) ?? []
      expect(opCols.length, name + ' 找不到「操作」列，断言会假绿').toBeGreaterThan(0)

      // 需要响应式取消固定的操作列：宽 180（automation 规则表）/ 330（data-sources）。
      // 宽 100 的是 automation 事件表操作列 —— 实测不遮挡（fixedW 100 << 表格盒 280/310，
      // 「确认执行」按钮中心恒落在粘性区外，48/48 命中自身），保持静态 fixed。
      const needReactive = opCols.filter((c) => /width="(180|330)"/.test(c))
      const staticKept = opCols.filter((c) => !/width="(180|330)"/.test(c))

      expect(
        needReactive.length,
        name + ' 没有找到需要响应式取消固定的操作列（width 180/330）—— 说明模板结构已变，' +
          '本断言失去对象（分母为 0）'
      ).toBeGreaterThan(0)
      for (const col of needReactive) {
        // 必须保留原列宽（借机改密度同样是回归）
        expect(col, name + ' 操作列缺少原列宽 width="180|330"：' + col).toMatch(/width="(180|330)"/)
        // 必须是响应式 fixed（? 是正则元字符，必须转义）
        expect(
          col,
          name + ' 操作列未使用响应式 fixed（窄屏 false / 桌面 right）：' + col
        ).toMatch(/:fixed="isMobile \? false : 'right'"/)
        // 不得再写死
        expect(col, name + ' 操作列不得再写死 fixed="right"：' + col).not.toMatch(/\sfixed="right"/)
      }
      // 豁免列必须仍是静态 fixed="right"（若被顺手改掉，说明修复范围失控）
      for (const col of staticKept) {
        expect(col, name + ' 实测不遮挡的窄操作列被改动，超出 F26 修复范围：' + col)
          .toMatch(/\sfixed="right"/)
      }
    }
  })
})
