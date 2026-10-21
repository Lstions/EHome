import { describe, expect, it, afterEach, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { config, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import source from '@/views/edge-device/shared/HistoryChartSection.vue?raw'
import HistoryChartSection from '@/views/edge-device/shared/HistoryChartSection.vue'

/**
 * 逆变器「历史数据」卡「分组 tab + 单图」契约（2026-10-21 用户要求）。
 *
 * 用户原话：「逆变器图表下拉框改为 tab 点击切换图表显示，交互效率更高，同时按钮与图表
 * 大小对比比例异常大」。改前桌面是 7 个子图**纵向平铺**（7 × 260px ≈ 1900px），窄屏是
 * el-select 下拉（换组两步：展开 + 选择）。
 *
 * 本文件锁五件事：
 *   ① 两种视口**结构统一**：一行分组 tab（全部可见）+ 单张图，同屏只渲染 1 个 echarts 实例；
 *      不得回退成 chart-grid 平铺或 el-select 下拉。
 *   ② 高度硬契约：桌面 320px / 窄屏 200px；窄屏 200 ≤ 220 ⇒ 进入 LineChart 的 isCompactChart
 *      分支（不画轴名 / 图例贴顶 / axisLabel.hideOverlap），这是窄屏不糊的机制。
 *   ③ tab 切换**不得重新发请求**（数据已在 chartSeries 内存里）。
 *   ④ 默认选中第一个有数据的分组；用户手选后不被数据到达覆盖。
 *   ⑤ 空态沿用 el-empty 并说明查询范围；不得用 ellipsis 隐藏分组名。
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
        reserveRightAxes: Number,
        showArea: Boolean,
      },
      setup(props) {
        return () => rh('div', {
          class: 'line-chart-stub',
          'data-height': props.height,
          'data-series-count': String((props.series as unknown[]).length),
          'data-reserve-axes': String(props.reserveRightAxes ?? ''),
        })
      },
    }),
  }
})

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

// ---- matchMedia 控制：组件用 (max-width: 768px) 判定窄屏（现在只影响图高） ----
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
  '温度 (°C)': ['pv_temp', 'inverter_temp', 'boost_temp', 'transformer_temp', 'max_temp'],
  '发电量 (kWh)': ['daily_energy', 'monthly_energy', 'yearly_energy', 'total_energy'],
  'BMS': ['bms_soc', 'bms_charge_current', 'bms_discharge_current', 'bms_charge_voltage_limit', 'bms_discharge_voltage_limit', 'bms_charge_current_limit', 'bms_temp'],
}
const GROUP_TITLES = Object.keys(GROUP_CATEGORIES)
const GROUP_SERIES_COUNT: Record<string, number> = Object.fromEntries(
  Object.entries(GROUP_CATEGORIES).map(([title, cats]) => [title, cats.length]),
)

function valueFor(cat: string): number {
  if (cat.endsWith('_voltage') || cat === 'bus_voltage') return 12.5
  if (cat === 'grid_frequency' || cat === 'output_frequency') return 50
  if (cat === 'output_load_percent' || cat === 'bms_soc') return 30
  if (cat.startsWith('temp')) return 28.1
  return 1.5
}

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

function tabTitles(wrapper: VueWrapper): string[] {
  return wrapper.findAll('.hist-tab').map((t) => t.text().trim())
}

async function clickTab(wrapper: VueWrapper, title: string) {
  const tab = wrapper.findAll('.hist-tab').find((t) => t.text().trim() === title)
  expect(tab, 'tab 不存在: ' + title).toBeTruthy()
  await tab!.trigger('click')
  await nextTick()
  await flushPromises()
}

describe('分组 tab + 单图（桌面与窄屏同一结构）', () => {
  for (const mobile of [false, true]) {
    const label = mobile ? '窄屏' : '桌面'
    it(label + '：1 行 tab + 1 张图，不再平铺、不再有下拉', async () => {
      const wrapper = await mountSection(mobile)
      expect(wrapper.find('.hist-tabs').exists()).toBe(true)
      expect(wrapper.find('.chart-grid').exists()).toBe(false)
      expect(wrapper.findAll('.chart-sub-section')).toHaveLength(0)
      expect(wrapper.find('.hist-mobile').exists()).toBe(false)
      expect(wrapper.find('select.el-select').exists()).toBe(false)
      expect(wrapper.findAll('.line-chart-stub')).toHaveLength(1)
      wrapper.unmount()
    })

    it(label + '：tab 数量/文案与分组一致（7 个，顺序不变）', async () => {
      const wrapper = await mountSection(mobile)
      expect(tabTitles(wrapper)).toEqual(GROUP_TITLES)
      wrapper.unmount()
    })

    it(label + '：图高硬契约（桌面 320px / 窄屏 200px，≤220 才进紧凑分支）', async () => {
      const wrapper = await mountSection(mobile)
      expect(wrapper.find('.line-chart-stub').attributes('data-height')).toBe(mobile ? '200px' : '320px')
      wrapper.unmount()
    })

    it(label + '：默认选中第一个有数据的分组，点击每个 tab 都能出图且横轴有点', async () => {
      const wrapper = await mountSection(mobile)
      const root = () => wrapper.find('.hist-tabbed')
      expect(root().attributes('data-group')).toBe('PV输入')
      expect(Number(root().attributes('data-point-count'))).toBeGreaterThan(0)
      for (const title of GROUP_TITLES) {
        await clickTab(wrapper, title)
        expect(root().attributes('data-group'), '切换后当前分组').toBe(title)
        expect(Number(root().attributes('data-series-count')), title).toBe(GROUP_SERIES_COUNT[title])
        expect(Number(root().attributes('data-point-count')), title).toBeGreaterThan(0)
        const stubs = wrapper.findAll('.line-chart-stub')
        expect(stubs, title).toHaveLength(1)
        expect(stubs[0].attributes('data-series-count'), title).toBe(String(GROUP_SERIES_COUNT[title]))
        expect(wrapper.findAll('.hist-tab.is-active')).toHaveLength(1)
        expect(wrapper.find('.hist-tab.is-active').text().trim()).toBe(title)
      }
      wrapper.unmount()
    })
  }

  it('active tab 的 aria-selected 为 true，其余为 false（读屏可感知当前分组）', async () => {
    const wrapper = await mountSection(false)
    let selected = wrapper.findAll('.hist-tab').map((t) => t.attributes('aria-selected'))
    expect(selected[0]).toBe('true')
    expect(selected.slice(1).every((v) => v === 'false')).toBe(true)
    await clickTab(wrapper, '电池')
    selected = wrapper.findAll('.hist-tab').map((t) => t.attributes('aria-selected'))
    expect(selected[GROUP_TITLES.indexOf('电池')]).toBe('true')
    expect(selected.filter((v) => v === 'true')).toHaveLength(1)
    wrapper.unmount()
  })

  it('点击 tab 不得重新发请求（数据已在内存里，切组只换渲染）', async () => {
    const wrapper = await mountSection(false)
    const callsAfterMount = clientGet.mock.calls.length
    expect(callsAfterMount).toBe(1) // 只发 1 次 historical-batch
    for (const title of GROUP_TITLES) await clickTab(wrapper, title)
    expect(clientGet.mock.calls.length).toBe(callsAfterMount)
    wrapper.unmount()
  })

  it('用户手选分组后不被数据到达覆盖（重新拉数据后仍保持手选分组）', async () => {
    const wrapper = await mountSection(true)
    await clickTab(wrapper, 'BMS')
    expect(wrapper.find('.hist-tabbed').attributes('data-group')).toBe('BMS')
    // 模拟切时间档：走组件暴露的 fetchHistoryData 重新拉一次（数据到达 = 新的一组 chartSeries）
    await (wrapper.vm as unknown as { fetchHistoryData: () => Promise<void> }).fetchHistoryData()
    await flushPromises()
    await nextTick()
    await flushPromises()
    expect(clientGet.mock.calls.length).toBe(2)
    expect(wrapper.find('.hist-tabbed').attributes('data-group')).toBe('BMS')
    wrapper.unmount()
  })

  it('某分组无数据时默认落到下一个有数据的分组，tab 只列出有数据的分组', async () => {
    const wrapper = await mountSection(true, { omitCategories: GROUP_CATEGORIES['PV输入'] })
    expect(wrapper.find('.hist-tabbed').attributes('data-group')).toBe('电网')
    expect(tabTitles(wrapper)).toEqual(GROUP_TITLES.slice(1))
    wrapper.unmount()
  })
})

describe('空态：沿用 el-empty（不得回退成空白）', () => {
  it('两种视口在无历史数据时都渲染可见空态，说明查询范围与可能原因', async () => {
    for (const mobile of [true, false]) {
      const wrapper = await mountSection(mobile, { omitCategories: Object.values(GROUP_CATEGORIES).flat() })
      expect(wrapper.find('.hist-tabs').exists()).toBe(false)
      expect(wrapper.findAll('.line-chart-stub')).toHaveLength(0)
      const empty = wrapper.find('.el-empty')
      expect(empty.exists(), String(mobile)).toBe(true)
      expect(empty.text()).toContain('当前时间范围内暂无历史数据')
      expect(empty.text()).toContain('已查询')
      expect(empty.text()).toContain('可能原因')
      wrapper.unmount()
    }
  })
})

describe('源码契约：tab 结构 / 高度 / 不得用 ellipsis 隐藏分组名', () => {
  const code = () => source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/<!--[\s\S]*?-->/g, '')

  it('tab 用原生 button + role=tablist/tab（不是 el-select，也不是 el-tabs 翻页器）', () => {
    expect(code()).toContain('role="tablist"')
    expect(code()).toContain('role="tab"')
    expect(code()).toContain('@click="onGroupSelect(g.title)"')
    expect(code()).not.toContain('el-select')
  })

  it('没有回退成 7 图平铺 / 窄屏下拉的老结构', () => {
    expect(code()).not.toContain('chart-grid')
    expect(code()).not.toContain('chart-sub-section')
    expect(code()).not.toContain('hist-mobile')
    expect(code()).not.toContain('hist-group-select')
    expect(code()).toContain('class="hist-tabbed"')
  })

  it('高度常量 320px / 200px 与窄屏 media query 同时存在', () => {
    expect(code()).toContain("'320px'")
    expect(code()).toContain("'200px'")
    expect(code()).toContain('@media (max-width: 768px)')
    expect(code()).toContain('flex-wrap: wrap')
  })

  it('不得给分组 tab / 图表加 ellipsis 截断', () => {
    expect(code()).not.toMatch(/text-overflow:\s*ellipsis/)
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
    expect(removed).toBeGreaterThan(0)
  })
})
