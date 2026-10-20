import { describe, expect, it, afterEach, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { config, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import source from '@/views/edge-device/shared/HistoryChartSection.vue?raw'
import HistoryChartSection from '@/views/edge-device/shared/HistoryChartSection.vue'

/**
 * 逆变器「历史数据」卡「窄屏单图 + 分组下拉」契约（2026-10-20，393 生产实测 2441px / 7 个 277×260 画布）。
 *
 * 本文件锁四件事：
 *   ① 窄屏（≤768px）：**单图 + el-select 分组下拉**（7 个分组、默认第一个有数据的分组、图高 150px）。
 *      150px ≤ 220 会进入 LineChart 的 isCompactChart 分支（不画轴名 / 图例贴顶 / axisLabel.hideOverlap）——
 *      这正是治「轴名压图例 / 图例翻页 1/5 / X 轴刻度挤成乱码」三个症状的机制，所以高度是硬契约。
 *   ② 分组切换**不得重新发请求**（数据已在 chartSeries 内存里，切换只换渲染）。
 *   ③ 桌面（>768px）**结构完全不动**：7 个 .chart-sub-section 平铺 + 图高 260px + 无下拉。
 *      这是「反向防回退」断言 —— 窄屏改造一旦泄漏到桌面，这里必须红。
 *   ④ 空态沿用 el-empty（不得回退成空白），且窄屏样式全部写在 @media (max-width: 768px) 内、
 *      不得给图表/选项加 ellipsis 截断。
 *
 * happy-dom 无 canvas：LineChart / echarts 必须 mock（真实 echarts.init 会抛），
 * mock 后正好可以断言组件传下去的 height / series。
 */

const { clientGet } = vi.hoisted(() => ({ clientGet: vi.fn() }))

vi.mock('@/api/client', () => ({
  default: { get: (...args: unknown[]) => clientGet(...args) },
  ApiError: class ApiError extends Error {},
  isApiErrorCode: () => false,
}))

vi.mock('@/components/charts/LineChart.vue', async () => {
  const { defineComponent: dc, h: rh } = await import('vue')
  return {
    default: dc({
      name: 'LineChart',
      props: {
        series: { type: Array, default: () => [] },
        data: { type: Array, default: () => [] },
        height: { type: String, default: '' },
        yAxisMin: Number,
        yAxisMax: Number,
        showArea: Boolean,
      },
      setup(props) {
        return () => rh('div', {
          class: 'line-chart-stub',
          'data-height': props.height,
          'data-series-count': String((props.series as unknown[]).length),
        })
      },
    }),
  }
})

// ElSelect / ElOption 走 test-setup.ts 的全局 stub（渲染成原生 select/option，便于 setValue 断言）
// ElEmpty 全局 stub 只渲染默认插槽、不渲染 #description 插槽；空态文案在 #description 里，
// 故这里用本地 stub 渲染 description 插槽，否则「空态文案可见」这条断言测的就不是真实内容。
const ElEmptyStub = defineComponent({
  name: 'ElEmpty',
  props: { description: String, imageSize: Number },
  setup(props, { slots }) {
    return () => h('div', { class: 'el-empty' }, [
      h('div', { class: 'el-empty__image' }),
      h('div', { class: 'el-empty__description' }, slots.description?.() ?? props.description),
    ])
  },
})
config.global.components['ElEmpty'] = ElEmptyStub
config.global.components['el-empty'] = ElEmptyStub

// ---- matchMedia 控制：组件用 (max-width: 768px) 判定窄屏 ----
const originalMatchMedia = window.matchMedia
function setViewportMobile(mobile: boolean) {
  const stub = (query: string) => ({
    matches: mobile,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })
  ;(window as unknown as { matchMedia: typeof window.matchMedia }).matchMedia = stub as unknown as typeof window.matchMedia
}
afterEach(() => {
  ;(window as unknown as { matchMedia: typeof window.matchMedia }).matchMedia = originalMatchMedia
  clientGet.mockReset()
})

/** 逆变器 7 个预定义分组 → 每组的 category 列表（与组件 inverterChartGroups 一一对应） */
const GROUP_CATEGORIES: Record<string, string[]> = {
  'PV输入': ['pv1_voltage', 'pv1_current', 'pv1_power', 'pv2_voltage', 'pv2_current', 'pv2_power'],
  '电网': ['grid_voltage', 'grid_frequency'],
  '输出': ['output_voltage', 'output_frequency', 'output_apparent_power', 'output_active_power', 'output_load_percent'],
  '电池': ['battery_voltage', 'battery_capacity', 'battery_charge_current', 'battery_discharge_current', 'bus_voltage'],
  '温度 (°C)': ['pv_temp', 'inverter_temp', 'boost_temp', 'transformer_temp', 'max_temp', 'pv2_temp', 'dc_rectifier_temp'],
  '发电量 (kWh)': ['daily_energy', 'monthly_energy', 'yearly_energy', 'total_energy'],
  'BMS': ['bms_soc', 'bms_charge_current', 'bms_discharge_current', 'bms_charge_voltage_limit', 'bms_discharge_voltage_limit', 'bms_charge_current_limit', 'bms_temp'],
}
const GROUP_TITLES = Object.keys(GROUP_CATEGORIES)
const GROUP_SERIES_COUNT = Object.fromEntries(
  Object.entries(GROUP_CATEGORIES).map(([title, cats]) => [title, cats.length]),
)

function valueFor(cat: string): number {
  if (cat.endsWith('_voltage') || cat === 'bus_voltage') return 12.5
  if (cat === 'grid_frequency' || cat === 'output_frequency') return 50
  if (cat === 'output_load_percent' || cat === 'bms_soc') return 30
  if (cat.startsWith('temp')) return 28.1
  return 1.5
}

/**
 * 历史数据 fixture：batch API 返回 categories 对应的 series（每条 1 个数据点）。
 * omitCategories：这些 category 无数据（用于构造「首组无数据」/ 全空场景）。
 */
function mockHistory(opts: { omitCategories?: string[] } = {}) {
  const omit = new Set(opts.omitCategories ?? [])
  clientGet.mockImplementation(async (_url: string, cfg: { params?: { categories?: string } }) => {
    const cats = String(cfg?.params?.categories ?? '').split(',').filter(Boolean)
    const data = cats
      .filter((c) => !omit.has(c))
      .map((cat) => ({
        category: cat,
        data: [{ created_at: '2026-10-20T00:00:00.000Z', value: valueFor(cat) }],
      }))
    return { code: 0, message: 'ok', data }
  })
}

async function mountSection(mobile: boolean, opts: { omitCategories?: string[] } = {}): Promise<VueWrapper> {
  setViewportMobile(mobile)
  mockHistory(opts)
  const wrapper = mount(HistoryChartSection, {
    props: { deviceId: 15, deviceType: 'techfine_inverter', deviceTypeText: '逆变器' },
  })
  await flushPromises()
  await nextTick()
  await flushPromises()
  await nextTick()
  return wrapper
}

function optionLabels(wrapper: VueWrapper): string[] {
  return wrapper.findAll('select.el-select option')
    .map((o) => o.text().trim())
    .filter((t) => t.length > 0)
}

describe('窄屏（≤768px）：历史数据 = 单图 + 分组下拉（2441 → 单图）', () => {
  it('不再平铺 7 个子图，改为 1 个 el-select + 1 张 150px 图', async () => {
    const wrapper = await mountSection(true)
    expect(wrapper.findAll('.chart-sub-section')).toHaveLength(0)
    expect(wrapper.find('.hist-mobile').exists()).toBe(true)
    expect(wrapper.find('.hist-group-select').exists()).toBe(true)
    const charts = wrapper.findAll('.line-chart-stub')
    expect(charts).toHaveLength(1)
    expect(charts[0].attributes('data-height')).toBe('150px')
  })

  it('下拉有且只有 7 个分组（沿用 chartSubGroups 的 title 作为 label）', async () => {
    const wrapper = await mountSection(true)
    const labels = optionLabels(wrapper)
    expect(labels).toEqual(GROUP_TITLES)
    expect(labels).toHaveLength(7)
  })

  it('默认选中第一个有数据的分组（PV输入），切换 7 个分组都能出图且横轴有点', async () => {
    const wrapper = await mountSection(true)
    expect(wrapper.find('.hist-mobile').attributes('data-group')).toBe('PV输入')
    expect(Number(wrapper.find('.hist-mobile').attributes('data-point-count'))).toBeGreaterThan(0)
    for (const title of GROUP_TITLES) {
      await wrapper.find('select.el-select').setValue(title)
      await nextTick()
      const mobile = wrapper.find('.hist-mobile')
      expect(mobile.attributes('data-group'), '切换后当前分组').toBe(title)
      expect(Number(mobile.attributes('data-series-count')), title).toBe(GROUP_SERIES_COUNT[title])
      expect(Number(mobile.attributes('data-point-count')), title).toBeGreaterThan(0)
      const stub = wrapper.find('.line-chart-stub')
      expect(stub.attributes('data-series-count'), title).toBe(String(GROUP_SERIES_COUNT[title]))
      expect(stub.attributes('data-height'), title).toBe('150px')
    }
  })

  it('默认值跟随数据：PV输入无数据时选第一个有数据的分组（电网）', async () => {
    const wrapper = await mountSection(true, { omitCategories: GROUP_CATEGORIES['PV输入'] })
    expect(wrapper.find('.hist-mobile').attributes('data-group')).toBe('电网')
    expect(optionLabels(wrapper)).toEqual(GROUP_TITLES.slice(1))
  })

  it('分组切换不得重新发请求（数据已在内存里，切组只换渲染）', async () => {
    const wrapper = await mountSection(true)
    const callsAfterMount = clientGet.mock.calls.length
    expect(callsAfterMount).toBe(1) // 只发 1 次 historical-batch
    for (const title of GROUP_TITLES) {
      await wrapper.find('select.el-select').setValue(title)
      await nextTick()
      await flushPromises()
    }
    expect(clientGet.mock.calls.length).toBe(callsAfterMount)
  })

  it('手动切换后不会被数据到达覆盖（用户选择优先）', async () => {
    const wrapper = await mountSection(true)
    await wrapper.find('select.el-select').setValue('BMS')
    await nextTick()
    // 触发一次时间范围切换：数据重新加载后仍保持用户选的 BMS 分组
    const radios = wrapper.findAll('input[type="radio"]')
    if (radios.length > 0) {
      await radios[0].setValue(true)
      await flushPromises()
      await nextTick()
    }
    expect(wrapper.find('.hist-mobile').attributes('data-group')).toBe('BMS')
  })
})

describe('反向防回退：桌面（>768px）结构必须与改前一致', () => {
  it('桌面仍平铺 7 个 .chart-sub-section，没有下拉、只有 7 张图', async () => {
    const wrapper = await mountSection(false)
    expect(wrapper.find('.chart-grid').exists()).toBe(true)
    expect(wrapper.findAll('.chart-sub-section')).toHaveLength(7)
    expect(wrapper.find('.hist-mobile').exists()).toBe(false)
    expect(wrapper.find('.hist-group-select').exists()).toBe(false)
    expect(wrapper.findAll('.line-chart-stub')).toHaveLength(7)
  })

  it('桌面 7 个子图标题与高度保持原契约（260px）', async () => {
    const wrapper = await mountSection(false)
    const titles = wrapper.findAll('.chart-sub-title').map((t) => t.text().trim())
    expect(titles).toEqual(GROUP_TITLES)
    const stubs = wrapper.findAll('.line-chart-stub')
    expect(stubs).toHaveLength(7)
    for (const s of stubs) expect(s.attributes('data-height')).toBe('260px')
  })
})

describe('空态：沿用 el-empty（不得回退成空白）', () => {
  it('窄屏/桌面在无历史数据时都渲染可见空态，说明查询范围与可能原因', async () => {
    for (const mobile of [true, false]) {
      const wrapper = await mountSection(mobile, {
        omitCategories: Object.values(GROUP_CATEGORIES).flat(),
      })
      expect(wrapper.find('.hist-mobile').exists()).toBe(false)
      expect(wrapper.find('.chart-sub-section').exists()).toBe(false)
      const empty = wrapper.find('.el-empty')
      expect(empty.exists(), String(mobile)).toBe(true)
      expect(empty.text()).toContain('当前时间范围内暂无历史数据')
      expect(empty.text()).toContain('已查询')
      expect(empty.text()).toContain('可能原因')
    }
  })
})

describe('源码契约：窄屏样式只在 ≤768px 生效 + 不得 ellipsis 截断', () => {
  it('窄屏结构分支使用 isMobileViewport（不是纯 CSS 隐藏）', () => {
    expect(source).toContain('const isMobileViewport = ref(')
    expect(source).toMatch(/v-if="!isMobileViewport && chartSubGroups.length > 0" class="chart-grid"/)
    expect(source).toMatch(/v-else-if="isMobileViewport && chartSubGroups.length > 0"/)
    expect(source).toContain('class="hist-group-select"')
  })

  it('高度硬契约：窄屏 150px（≤220 ⇒ isCompactChart）、桌面 260px；桌面分支结构未动', () => {
    expect(source).toContain('height="150px"')
    expect(source).toContain('height="260px"')
    // 桌面分支仍是原来的 v-for chartSubGroups + .chart-sub-section 结构
    expect(source).toMatch(/v-for="group in chartSubGroups"\s*:key="group.title"\s*class="chart-sub-section"/)
    expect(source).toContain('@media (max-width: 768px)')
  })

  it('新增窄屏样式全部写在 @media (max-width: 768px) 内', () => {
    const mediaStart = source.indexOf('@media (max-width: 768px)')
    expect(mediaStart).toBeGreaterThan(0)
    const media = source.slice(mediaStart)
    for (const marker of [
      '.hist-group-select { width: 100%; }',
      '.hist-mobile-chart { height: 150px; }',
      '.header-controls { display: contents; }',
      'min-height: 44px',
      'text-overflow: clip',
    ]) {
      expect(media, marker).toContain(marker)
    }
    // 150 的容器高度只在媒体查询里出现；桌面 260px 在模板里、不在媒体查询里被改成别的值
    const beforeMedia = source.slice(0, mediaStart)
    expect(beforeMedia).not.toContain('.hist-mobile-chart')
    expect(source.match(/height="260px"/g)?.length).toBe(1)
  })

  it('不得给图表/下拉加 ellipsis 截断（先剥注释再断言，避免注释里解释文字假红）', () => {
    const code = source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/<!--[\s\S]*?-->/g, '')
    expect(code).not.toMatch(/text-overflow:\s*ellipsis/)
    expect(code).toContain('text-overflow: clip')
  })
})

describe('移动端事件解绑（内存泄漏护栏）', () => {
  it('组件卸载时移除 matchMedia 监听', async () => {
    setViewportMobile(true)
    mockHistory()
    let removed = 0
    const stub = (query: string) => ({
      matches: true,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => { removed += 1 },
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    })
    ;(window as unknown as { matchMedia: typeof window.matchMedia }).matchMedia = stub as unknown as typeof window.matchMedia
    const wrapper = mount(HistoryChartSection, {
      props: { deviceId: 15, deviceType: 'techfine_inverter', deviceTypeText: '逆变器' },
    })
    await flushPromises()
    await nextTick()
    wrapper.unmount()
    expect(removed).toBe(1)
  })
})
