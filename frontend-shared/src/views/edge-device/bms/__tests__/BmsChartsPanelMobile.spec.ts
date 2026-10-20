import { describe, expect, it, afterEach, vi } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { config, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import source from '@/views/edge-device/bms/BmsChartsPanel.vue?raw'
import BmsChartsPanel from '@/views/edge-device/bms/BmsChartsPanel.vue'

/**
 * BMS 图表面板移动端改版契约（2026-10-20，方案 B：电芯历史 592→≤300、运行趋势 1435→≤320）。
 *
 * 本文件锁三件事：
 *   ① 窄屏：运行趋势 = 单图 + el-select 量纲切换器（5 个选项，数据一次拉取、切换不重新请求）；
 *      电芯历史 = 16 个勾选框收进 el-popover（触发器「已选 N/16」）、图高 160px。
 *   ② 桌面（>768px）**结构完全不动**：5 个 .trend-sub 平铺 + 16 个 checkbox 平排 + 原图高 280/200。
 *      这是「反向防回退」断言 —— 移动端改造一旦泄漏到桌面，这里必须红。
 *   ③ 每张图独立 loading/empty（单图失败不影响另一张）；慢速加载（slow 档）时切换器仍完整、
 *      选项文案不被 ellipsis 截断。
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

// ---- Element Plus 局部 stub 覆盖（test-setup.ts 的通用 ElPopover 会丢掉 #reference 插槽）----
const ElPopoverStub = defineComponent({
  name: 'ElPopover',
  props: { width: [Number, String], placement: String, trigger: String, popperClass: String },
  setup(_, { slots }) {
    return () => h('div', { class: 'el-popover-stub' }, [
      h('span', { class: 'el-popover-reference' }, slots.reference?.()),
      h('div', { class: 'el-popover-content' }, slots.default?.()),
    ])
  },
})
const ElCheckboxGroupStub = defineComponent({
  name: 'ElCheckboxGroup',
  props: { modelValue: { type: Array, default: () => [] } },
  emits: ['update:modelValue'],
  setup(_, { slots }) {
    return () => h('div', { class: 'el-checkbox-group' }, slots.default?.())
  },
})
config.global.components['ElPopover'] = ElPopoverStub
config.global.components['el-popover'] = ElPopoverStub
config.global.components['ElCheckboxGroup'] = ElCheckboxGroupStub
config.global.components['el-checkbox-group'] = ElCheckboxGroupStub

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
})

// ---- 历史数据 fixture：每个 category 一条点，保证 5 个量纲都有数据 ----
function valueFor(cat: string): number {
  if (cat.startsWith('cell_voltage_')) return 3.3
  if (cat === 'total_voltage') return 52.1
  if (cat === 'cell_voltage_max') return 3.35
  if (cat === 'cell_voltage_min') return 3.25
  if (cat === 'current') return 10.5
  if (cat === 'rsoc') return 7
  if (cat === 'remaining_capacity') return 30
  if (cat.startsWith('temperature_')) return 28.1
  return 1
}
function mockHistory(opts: { emptyCats?: string[] } = {}) {
  const emptyCats = opts.emptyCats ?? []
  clientGet.mockImplementation(async (_url: string, cfg: { params?: { categories?: string } }) => {
    const cats = String(cfg?.params?.categories ?? '').split(',').filter(Boolean)
    const data = cats
      .filter((c) => !emptyCats.includes(c))
      .map((cat) => ({ category: cat, data: [{ created_at: '2026-10-20T00:00:00.000Z', value: valueFor(cat) }] }))
    return { code: 0, message: 'ok', data }
  })
}

async function mountPanel(mobile: boolean): Promise<VueWrapper> {
  setViewportMobile(mobile)
  mockHistory()
  const wrapper = mount(BmsChartsPanel, { props: { deviceId: 14, cellCount: 16, online: true } })
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

describe('窄屏（≤768px）：运行趋势 = 单图 + 量纲切换器（1435 → ≤320）', () => {
  it('不再平铺 5 个子图，改为 1 个 el-select + 1 张图', async () => {
    const wrapper = await mountPanel(true)
    expect(wrapper.findAll('.trend-sub')).toHaveLength(0)
    expect(wrapper.find('.trend-mobile').exists()).toBe(true)
    expect(wrapper.find('.trend-dim-select').exists()).toBe(true)
    const trendCard = wrapper.findAll('.charts-row .chart-card')[1]
    const charts = trendCard.findAll('.line-chart-stub')
    expect(charts).toHaveLength(1)
    expect(charts[0].attributes('data-height')).toBe('196px')
  })

  it('量纲切换器有且只有 5 个选项：总电压 / 单体电压 / 电流 / 电池状态 / 温度', async () => {
    const wrapper = await mountPanel(true)
    const labels = optionLabels(wrapper)
    expect(labels).toEqual(['总电压', '单体电压', '电流', '电池状态', '温度'])
    expect(labels).toHaveLength(5)
  })

  it('默认选中第一个有数据的分组（总电压），切换 5 个量纲都能出图且横轴有点', async () => {
    const wrapper = await mountPanel(true)
    expect(wrapper.find('.trend-mobile').attributes('data-dim')).toBe('total_voltage')
    expect(Number(wrapper.find('.trend-mobile').attributes('data-point-count'))).toBeGreaterThan(0)
    const expectedSeries: Record<string, number> = {
      total_voltage: 1, cell_voltage: 2, current: 1, status: 2, temperature: 3,
    }
    for (const [dim, seriesCount] of Object.entries(expectedSeries)) {
      await wrapper.find('select.el-select').setValue(dim)
      await nextTick()
      const mobile = wrapper.find('.trend-mobile')
      expect(mobile.attributes('data-dim'), '切换后当前量纲').toBe(dim)
      expect(Number(mobile.attributes('data-series-count')), dim).toBe(seriesCount)
      expect(Number(mobile.attributes('data-point-count')), dim).toBeGreaterThan(0)
      const stub = wrapper.findAll('.charts-row .chart-card')[1].find('.line-chart-stub')
      expect(stub.attributes('data-series-count'), dim).toBe(String(seriesCount))
    }
  })

  it('默认值跟随数据：总电压无数据时选下一个有数据的分组（单体电压）', async () => {
    setViewportMobile(true)
    mockHistory({ emptyCats: ['total_voltage'] })
    const wrapper = mount(BmsChartsPanel, { props: { deviceId: 14, cellCount: 16, online: true } })
    await flushPromises()
    await nextTick()
    await flushPromises()
    expect(wrapper.find('.trend-mobile').attributes('data-dim')).toBe('cell_voltage')
  })
})

describe('窄屏：电芯电压历史趋势（592 → ≤300）', () => {
  it('16 个勾选框收进 el-popover，不在页面平铺；触发器显示「已选 16/16」', async () => {
    const wrapper = await mountPanel(true)
    const cellCard = wrapper.findAll('.charts-row .chart-card')[0]
    expect(cellCard.find('.cell-picker').exists()).toBe(false)
    const trigger = cellCard.find('.cell-picker-trigger')
    expect(trigger.exists()).toBe(true)
    expect(trigger.text()).toContain('已选 16/16')
    expect(cellCard.findAll('.cell-picker-pop .el-checkbox')).toHaveLength(16)
    const chart = cellCard.find('.line-chart-stub')
    expect(chart.attributes('data-height')).toBe('160px')
  })
})

describe('反向防回退：桌面（>768px）结构必须与改前一致', () => {
  it('桌面仍平铺 5 个 .trend-sub（单图 + 切换器只允许出现在窄屏）', async () => {
    const wrapper = await mountPanel(false)
    expect(wrapper.findAll('.trend-sub')).toHaveLength(5)
    expect(wrapper.find('.trend-mobile').exists()).toBe(false)
    expect(wrapper.find('.trend-dim-select').exists()).toBe(false)
  })

  it('桌面 5 个子图标题与高度保持原契约（200px 图高）', async () => {
    const wrapper = await mountPanel(false)
    const titles = wrapper.findAll('.trend-sub-title').map((t) => t.text().trim())
    expect(titles).toEqual(['总电压 (V)', '单体电压 (V)', '电流 (A)', '电池状态', '温度 (℃)'])
    const stubs = wrapper.findAll('.charts-row .chart-card')[1].findAll('.line-chart-stub')
    expect(stubs).toHaveLength(5)
    for (const s of stubs) expect(s.attributes('data-height')).toBe('200px')
  })

  it('桌面仍平排 16 个 checkbox，没有弹层触发器；电芯图高 280px', async () => {
    const wrapper = await mountPanel(false)
    const cellCard = wrapper.findAll('.charts-row .chart-card')[0]
    expect(cellCard.findAll('.cell-picker .el-checkbox')).toHaveLength(16)
    expect(cellCard.find('.cell-picker-trigger').exists()).toBe(false)
    expect(cellCard.find('.line-chart-stub').attributes('data-height')).toBe('280px')
  })
})

describe('降级：每张图独立 loading/empty，单图失败不影响另一张', () => {
  it('电芯历史无数据时只该卡空态可见，运行趋势仍正常出图', async () => {
    setViewportMobile(true)
    const cellCats = Array.from({ length: 16 }, (_, i) => 'cell_voltage_' + (i + 1))
    mockHistory({ emptyCats: cellCats })
    const wrapper = mount(BmsChartsPanel, { props: { deviceId: 14, cellCount: 16, online: true } })
    await flushPromises()
    await nextTick()
    await flushPromises()
    const cellCard = wrapper.findAll('.charts-row .chart-card')[0]
    const trendCard = wrapper.findAll('.charts-row .chart-card')[1]
    expect(cellCard.find('.chart-state').text()).toBe('暂无电芯电压历史数据')
    expect(cellCard.find('.cell-picker-trigger').exists()).toBe(false)
    expect(trendCard.find('.trend-dim-select').exists()).toBe(true)
    expect(trendCard.find('.line-chart-stub').exists()).toBe(true)
  })
})

describe('慢速加载（slow 档）与防截断', () => {
  it('slow 档：请求悬而未决时切换器仍在、5 个选项文案完整，无 ellipsis 截断', async () => {
    setViewportMobile(true)
    clientGet.mockImplementation(() => new Promise(() => {}))
    const wrapper = mount(BmsChartsPanel, { props: { deviceId: 14, cellCount: 16, online: true } })
    await nextTick()
    const trendCard = wrapper.findAll('.charts-row .chart-card')[1]
    expect(trendCard.find('.trend-dim-select').exists(), 'slow 档下切换器不应消失').toBe(true)
    expect(optionLabels(wrapper)).toEqual(['总电压', '单体电压', '电流', '电池状态', '温度'])
    expect(trendCard.find('.trend-sub-state.is-loading').text()).toBe('加载中…')
    // 先剥注释再断言：源码注释里**解释**为什么要收回 EP 默认 ellipsis，直接对全文断言会假红
    const code = source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/<!--[\s\S]*?-->/g, '')
    expect(code).not.toMatch(/text-overflow:\s*ellipsis/)
    expect(code, '量纲切换器显式收回 EP 的 ellipsis').toContain('text-overflow: clip')
    for (const label of ['总电压', '单体电压', '电流', '电池状态', '温度']) {
      expect(optionLabels(wrapper), label).toContain(label)
    }
  })
})

describe('源码契约：移动端样式只在窄屏生效', () => {
  it('窄屏结构分支使用 isMobileViewport（而不是纯 CSS 隐藏）', () => {
    expect(source).toContain('const isMobileViewport = ref(')
    expect(source).toMatch(/v-if="!isMobileViewport" class="trend-grid"/)
    expect(source).toContain('v-if="isMobileViewport && cellAll.length > 0"')
    expect(source).toMatch(/v-if="!isMobileViewport" class="cell-picker"/)
  })

  it('新增移动端样式全部写在 @media (max-width: 768px) 内', () => {
    const media = source.slice(source.indexOf('@media (max-width: 768px)'))
    for (const marker of ['.chart-box { height: 160px; }', '.chart-state { height: 160px; }', '.trend-dim-bar', '.cell-picker-trigger { min-height: 44px; }']) {
      expect(media, marker).toContain(marker)
    }
    const beforeMedia = source.slice(0, source.indexOf('@media (max-width: 768px)'))
    expect(beforeMedia).toContain('.chart-box { height: 280px; }')
    expect(beforeMedia).toContain('height="200px"')
  })

  it('量纲定义覆盖 5 组且与旧页分组一致（切换不增加请求）', () => {
    for (const label of ['总电压', '单体电压', '电流', '电池状态', '温度']) {
      expect(source).toContain("label: '" + label + "'")
    }
    expect(source).toContain('metricAll.value.filter((s) => s.category && g.categories.includes(s.category))')
  })
})