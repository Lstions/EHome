import { describe, expect, it, beforeEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createPinia, setActivePinia } from 'pinia'
import { useBreadcrumbStore } from '@/stores/breadcrumb'
import {
  COMPACT_DEFAULT_GRID_TOP_PX,
  LEGEND_GRID_GAP_PX,
  LEGEND_ROW_HEIGHT_PX,
  LEGEND_TOP_PX,
  TOP_RESERVED_PX,
  computeChartTopLayout,
  estimateLegendTextWidth,
  shouldShowYAxisName,
} from '@/components/charts/LineChart.vue'

/**
 * 2026-10-21 用户反馈两条：
 *   ①「图表长度显示参差不齐」
 *   ②「顶部面包屑不应该显示『边缘设备详情』，应该显示边缘设备名称」
 *
 * ① 的真实原因不在容器：6 个 .chart-sub-section 的高度都严格 260px。
 *    差异藏在 ECharts 的 grid（plot 区）—— 改前实测同一屏 plot 宽
 *    615 / 688 / 555 / 628 / 723 / 622px、高 139 / 122 / 159 / 148 / 100px。
 *    成因两处：
 *      · containLabel:true 会按**本图标签实际宽度**往里收 ⇒ 左轴"300.0"与"54.9"不同宽，
 *        右轴 1 根与 3 根不同宽；
 *      · grid.top 未设，交给自动布局 ⇒ 随「轴名个数 + 图例行数」变化。
 *    修法：常规图改 containLabel:false + 像素边距，右边距按父级传入的
 *    组内最大右轴数（reserveRightAxes）预留 ⇒ 组内 plot rect 严格相等。
 *
 * ② 的失败模式：面包屑末段是路由推导出来的抽象称谓（「边缘设备详情」），
 *    而用户要的是具体实体名。名称是异步取到的 ⇒ 必须由页面登记，不能靠路由 meta。
 */
const read = (p: string) => readFileSync(resolve(process.cwd(), p), 'utf8')

const LINE_CHART = read('src/components/charts/LineChart.vue')
const HISTORY = read('src/views/edge-device/shared/HistoryChartSection.vue')
const LAYOUT = read('src/views/layout/MainLayout.vue')
const BMS = read('src/views/edge-device/bms/BmsDetailPage.vue')
const INVERTER = read('src/views/edge-device/inverter/InverterDetailPage.vue')
const GENERIC = read('src/views/edge-device/GenericDeviceDetail.vue')

describe('图表绘图区几何必须跨分组一致', () => {
  it('常规图用像素边距 + containLabel:false（否则标签宽度会参与收缩）', () => {
    // 反向守卫：containLabel:true 会让同页 plot 宽度随标签宽度变化，是本 bug 的成因之一
    expect(LINE_CHART).toContain('containLabel: false')
    expect(LINE_CHART).toMatch(/left: LEFT_RESERVED_PX/)
    expect(LINE_CHART).toMatch(/top: TOP_RESERVED_PX/)
    expect(LINE_CHART).toMatch(/bottom: BOTTOM_RESERVED_PX/)
  })

  it('上边距是显式常量，不再交给 ECharts 自动布局', () => {
    expect(LINE_CHART).toMatch(/const TOP_RESERVED_PX = \d+/)
    // 自动 top 会随轴名个数/图例行数变化 ⇒ 同屏 plot 高度不一
    expect(LINE_CHART).not.toMatch(/grid:\s*\{\s*left:/)
  })

  it('右边距按「本图右轴数」与「父级预留轴数」取大者 ⇒ 组内同宽', () => {
    expect(LINE_CHART).toMatch(/Math\.max\(rightAxisCount, props\.reserveRightAxes \?\? 0\)/)
    expect(LINE_CHART).toContain('reserveRightAxes?: number')
  })

  it('窄桌面按画布宽度夹取右边距（769px 时 canvas 仅约 453px）', () => {
    expect(LINE_CHART).toMatch(/const RIGHT_MAX_RATIO = 0\.\d+/)
    expect(LINE_CHART).toMatch(/Math\.min\(raw, Math\.max\(RIGHT_BASE_PX \* 2, canvasWidth \* RIGHT_MAX_RATIO\)\)/)
  })

  it('HistoryChartSection 算出组内最大右轴数并传给每张图', () => {
    expect(HISTORY).toContain('maxReserveRightAxes')
    expect(HISTORY).toContain(':reserve-right-axes="maxReserveRightAxes"')
    // 上限与 LineChart 的 MAX_Y_AXIS 同口径（4 轴 ⇒ 最多 3 根右轴）
    expect(HISTORY).toMatch(/LINE_CHART_MAX_Y_AXIS = 4/)
  })

  it('reserveRightAxes 变化会触发重绘（否则异步数据到达后仍用旧预留）', () => {
    expect(LINE_CHART).toMatch(/props\.reserveRightAxes\]/)
  })
})

describe('顶部面包屑末段显示实体名称', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('store 提供 set / clear，空串表示不覆盖', () => {
    const store = useBreadcrumbStore()
    expect(store.leafLabel).toBe('')
    store.setLeafLabel('  bms  ')
    expect(store.leafLabel).toBe('bms')
    store.clearLeafLabel()
    expect(store.leafLabel).toBe('')
  })

  it('MainLayout 只覆盖**最后一段**，中间段仍用模块名（保持可点回列表语义）', () => {
    expect(LAYOUT).toContain('breadcrumbStore.leafLabel')
    expect(LAYOUT).toMatch(/crumbs\[crumbs\.length - 1\] = \{ \.\.\.crumbs\[crumbs\.length - 1\], text: leaf \}/)
  })

  it('三个设备详情页都登记设备名，并在卸载时清空（避免串到别的页面）', () => {
    for (const [name, src] of [['BMS', BMS], ['逆变器', INVERTER], ['通用设备', GENERIC]] as const) {
      expect(src, name + ' 未登记面包屑末段').toContain('breadcrumbStore.setLeafLabel')
      expect(src, name + ' 未在卸载时清空').toContain('breadcrumbStore.clearLeafLabel')
      expect(src, name + ' 未引入 store').toContain("from '@/stores/breadcrumb'")
    }
  })

  it('登记的是 device.name（不是带占位符的展示名）', () => {
    for (const [name, src] of [['BMS', BMS], ['逆变器', INVERTER], ['通用设备', GENERIC]] as const) {
      expect(src, name + ' 用了占位符展示名').toMatch(/watch\(\s*\(\) => device\.value\?\.name/)
    }
  })
})

/**
 * 图表顶部布局纯函数契约（2026-10-21 用户反馈「图例压住绘图区与 Y 轴名」）。
 *
 * ## 缺陷（真机实测，逆变器「输出」组：6 条序列 5 种单位）
 *   · 图例固定 top=30、行高约 20px；plain 图例在宽度用尽时折行。
 *   · grid.top 原为常量 56（只够 1 行图例 + 轴名），图例折成 2 行后底边 ≈70 > 56
 *     ⇒ 图例压在绘图区与 Y 轴名上（实测底边 ≈66px）。
 *   · 多根右轴（offset 60/120/180）的轴名 nameLocation:'end' 在顶部互相重叠
 *     —— (V)(Hz)(VA)(W)(%) 挤成一团。
 *
 * ## 修法（本文件守护的契约）
 *   ① computeChartTopLayout：行数按「标记 + 文字宽度 + itemGap」对可用宽贪心折行；
 *      grid.top = 图例 top + 行数×行高 + 间隙，且**不低于** TOP_RESERVED_PX
 *      ⇒ 单行时仍为 56（改前像素不变），2 行 76、3 行 96。
 *   ② shouldShowYAxisName：右轴数 ≥ 2（总轴数 ≥ 3）时**所有轴名清空**（单位由图例承担）；
 *      右轴 ≤ 1 保留原行为（左轴显示、同单位只显示一次）；紧凑图一律不显示。
 *
 * 判据用真实函数调用（不是源码字符串包含），并对关键数字做硬断言。
 */
describe('computeChartTopLayout：图例行数 → grid.top', () => {
  /** 逆变器「输出」组：6 条序列 5 种单位（实测在 844px 画布折 2 行）。 */
  const SIX_ITEMS = [
    '输出电压 (V)',
    '输出频率 (Hz)',
    '输出视在功率 (VA)',
    '输出有功功率 (W)',
    '输出负载率 (%)',
    '输出电流 (A)',
  ]

  it('6 条 / 5 单位 / 844px：估出 2 行，grid.top = 76（> 56，图例底边不再压图）', () => {
    const layout = computeChartTopLayout({ canvasWidth: 844, items: SIX_ITEMS, height: '260px' })
    expect(layout.compact).toBe(false)
    expect(layout.legendRows, '实测 844px 下这 6 条图例折成 2 行（1 行会漏掉重叠缺陷）').toBe(2)
    expect(layout.legendTop).toBe(LEGEND_TOP_PX)
    // 图例底边 = top + 行数 × 行高
    const legendBottom = layout.legendTop + layout.legendRows * LEGEND_ROW_HEIGHT_PX
    expect(legendBottom, '图例底边').toBe(70)
    expect(layout.gridTop, 'grid.top 必须超过图例底边（改前 56 < 70 就是压图的根因）').toBeGreaterThan(legendBottom)
    expect(layout.gridTop).toBe(LEGEND_TOP_PX + 2 * LEGEND_ROW_HEIGHT_PX + LEGEND_GRID_GAP_PX)
    expect(layout.gridTop).toBe(76)
  })

  it('单行图例：grid.top 退回基线 56 —— 与改前逐像素一致（不得顺手抬高所有图）', () => {
    const layout = computeChartTopLayout({
      canvasWidth: 844,
      items: ['输出电压 (V)', '输出频率 (Hz)'],
      height: '260px',
    })
    expect(layout.legendRows).toBe(1)
    expect(layout.gridTop, '单行时 grid.top 必须仍是 56（改前像素基线）').toBe(TOP_RESERVED_PX)
    expect(layout.gridTop).toBe(56)
  })

  it('窄画布行数增加、grid.top 单调不减（不会因为容器变窄又压回图例）', () => {
    const wide = computeChartTopLayout({ canvasWidth: 844, items: SIX_ITEMS, height: '260px' })
    const narrow = computeChartTopLayout({ canvasWidth: 400, items: SIX_ITEMS, height: '260px' })
    expect(narrow.legendRows, '更窄的画布不可能用更少的行数').toBeGreaterThanOrEqual(wide.legendRows)
    expect(narrow.gridTop).toBeGreaterThanOrEqual(wide.gridTop)
    expect(narrow.legendRows).toBe(3)
    expect(narrow.gridTop).toBe(96)
    // 极窄（240px）时行数继续增加，grid.top 继续抬升
    const tiny = computeChartTopLayout({ canvasWidth: 240, items: SIX_ITEMS, height: '260px' })
    expect(tiny.legendRows).toBeGreaterThanOrEqual(narrow.legendRows)
    expect(tiny.gridTop).toBeGreaterThanOrEqual(narrow.gridTop)
  })

  it('画布宽度未知（未挂载 / happy-dom）时退回单行假设，不擅自抬高 plot', () => {
    for (const canvasWidth of [0, Number.NaN]) {
      const layout = computeChartTopLayout({ canvasWidth, items: SIX_ITEMS, height: '260px' })
      expect(layout.legendRows, '宽度未知时无法判定折行').toBe(1)
      expect(layout.gridTop).toBe(TOP_RESERVED_PX)
    }
    const empty = computeChartTopLayout({ canvasWidth: 844, items: [], height: '260px' })
    expect(empty.legendRows, '无图例项（单序列）只能是 1 行').toBe(1)
    expect(empty.gridTop).toBe(TOP_RESERVED_PX)
  })

  it('紧凑图（height ≤ 220）：grid.top 报告 60 且**不参与 option**（其它组件拿它当常量画告警线）', () => {
    const compact = computeChartTopLayout({ canvasWidth: 319, items: SIX_ITEMS, height: '150px' })
    expect(compact.compact).toBe(true)
    expect(compact.legendTop, '紧凑图图例贴顶 6px').toBe(6)
    expect(compact.gridTop, '紧凑图 grid.top 必须保持 ECharts 默认 60（改前常量，别处依赖）').toBe(COMPACT_DEFAULT_GRID_TOP_PX)
    // 边界：220 仍是紧凑图；221 进入常规分支
    expect(computeChartTopLayout({ canvasWidth: 844, items: SIX_ITEMS, height: '220px' }).compact).toBe(true)
    const justNormal = computeChartTopLayout({ canvasWidth: 844, items: SIX_ITEMS, height: '221px' })
    expect(justNormal.compact).toBe(false)
    expect(justNormal.gridTop).toBe(76)
    // 数字/字符串两种写法等价
    expect(computeChartTopLayout({ canvasWidth: 844, items: SIX_ITEMS, height: 260 }).gridTop).toBe(76)
  })

  it('图例项宽度估算：全角宽于半角，且总宽与行数判定一致（防止估算恒为 0 导致假绿）', () => {
    expect(estimateLegendTextWidth('输出电压 (V)')).toBeGreaterThan(estimateLegendTextWidth('abc'))
    const layout = computeChartTopLayout({ canvasWidth: 844, items: SIX_ITEMS, height: '260px' })
    expect(layout.legendItemWidths).toHaveLength(SIX_ITEMS.length)
    for (const width of layout.legendItemWidths) expect(width).toBeGreaterThan(40)
  })
})

describe('shouldShowYAxisName：多右轴时轴名不得互压', () => {
  const base = { compact: false, index: 0, unit: 'V', priorUnits: [] as string[], totalAxes: 1 }

  it('右轴数 ≥ 2（总轴数 ≥ 3）：所有轴名清空（单位由图例项承担）', () => {
    const units = ['V', 'Hz', 'VA', 'W', '%']
    for (let i = 0; i < units.length; i++) {
      expect(
        shouldShowYAxisName({ compact: false, index: i, unit: units[i], priorUnits: units.slice(0, i), totalAxes: units.length }),
        '第 ' + i + ' 根轴（totalAxes=' + units.length + '）仍画轴名 ⇒ nameLocation:end 会在顶部互压',
      ).toBe(false)
    }
    // 边界：总轴数 3（1 左 + 2 右）即触发清空
    expect(shouldShowYAxisName({ ...base, totalAxes: 3 })).toBe(false)
  })

  it('右轴 ≤ 1（总轴数 ≤ 2）保留原行为：左轴显示、同单位只显示一次', () => {
    expect(shouldShowYAxisName({ ...base, totalAxes: 1 }), '单轴要显示单位').toBe(true)
    expect(shouldShowYAxisName({ ...base, totalAxes: 2 }), '左右各一轴：左轴显示').toBe(true)
    // 同单位重复（1 左 + 1 右 均为 V）：右轴不再重复显示 (V)
    expect(shouldShowYAxisName({ ...base, index: 1, unit: 'V', priorUnits: ['V'], totalAxes: 2 })).toBe(false)
    // 不同单位：右轴显示自己的单位
    expect(shouldShowYAxisName({ ...base, index: 1, unit: 'Hz', priorUnits: ['V'], totalAxes: 2 })).toBe(true)
  })

  it('紧凑图一律不显示轴名（轴名会压住贴顶图例）', () => {
    expect(shouldShowYAxisName({ ...base, compact: true, totalAxes: 1 })).toBe(false)
    expect(shouldShowYAxisName({ ...base, compact: true, index: 1, unit: 'Hz', totalAxes: 2 })).toBe(false)
  })
})
