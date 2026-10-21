import { describe, expect, it, beforeEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createPinia, setActivePinia } from 'pinia'
import { useBreadcrumbStore } from '@/stores/breadcrumb'

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
