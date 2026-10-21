import { describe, expect, it } from 'vitest'
import lineChartSource from '@/components/charts/LineChart.vue?raw'
import tempTrendSource from '@/views/edge-device/bms/diag/TempTrendCard.vue?raw'

/**
 * LineChart「紧凑图」排版契约（2026-10-20）。
 *
 * 缺陷现象：窄屏小图（≤220px 高）里，ECharts 的 yAxis.name 默认 nameLocation:'end'，
 * 画在轴顶端 —— 正好是图例那一行，实测把「#1 (V)」压成「#1 (V)(V)」、
 * 「探头 1 (℃)」压成双层字（截图 frontend-shared/.logs/ui-fix/review-shots/legend-*.png）。
 *
 * 修法：紧凑图不画轴名（单位不丢 —— 图例项自带「#1 (V)」后缀），图例上移到 top 6。
 * 关键副作用约束：**不能动 grid.top** —— TempTrendCard 用 CSS 绝对定位画 45℃ 告警线，
 * 其 y 坐标按「ECharts grid 默认 top=60」换算；改了就会错位 20px。
 */
describe('LineChart 紧凑图排版（轴名不得压图例）', () => {
  it('按高度判定紧凑图（≤220px），常规大图不受影响', () => {
    expect(lineChartSource).toContain('const isCompactChart = computed(')
    expect(lineChartSource).toMatch(/h <= 220/)
  })

  it('紧凑图不画 y 轴名；常规图按单位写轴名，且同单位只在第一根轴写一次', () => {
    // 2026-10-21：「发电量」组实测有 3 根 kWh 轴，三处 (kWh) 在顶部挤在一起并压住刻度。
    // 现在紧凑图一律不写；常规图只在**该单位首次出现**的那根轴写，其余留空（单位仍由图例后缀表达）。
    expect(lineChartSource).toContain('name: isCompactChart.value || (i > 0 && axes.slice(0, i).some((a) => a.unit === axis.unit))')
    // 反向：常规分支的轴名逻辑必须还在（否则桌面多轴图失去单位标识）
    expect(lineChartSource).toContain('`(${axis.unit})`')
  })

  it('图例必须显式 bottom:auto 且不得带 undefined 键（2026-10-21 真机错位根因）', () => {
    // ECharts 6.1.0 legend 默认带 bottom ⇒ 只给 top 时图例被夹在 top..bottom 盒子外；
    // 而显式 undefined 的 itemWidth/itemHeight/fontSize 会被当成「已提供」参与行高计算。
    expect(lineChartSource).toContain("bottom: 'auto' as const,")
    // 图例的紧凑尺寸必须走「按需展开」，不能写成 xxx: cond ? 值 : undefined
    expect(lineChartSource).toContain('...(isCompactChart.value')
    expect(lineChartSource).toContain('{ itemWidth: 10, itemHeight: 8')
    expect(lineChartSource).not.toMatch(/itemWidth: isCompactChart\.value \? 10 : undefined/)
  })

  it('紧凑图图例上移到 top 6，常规图保持 30', () => {
    expect(lineChartSource).toMatch(/top: isCompactChart\.value \? 6 : 30/)
  })

  it('紧凑图 X 轴标签开启 hideOverlap（6 个时间刻度曾挤成 20:0000:0004:00…）', () => {
    expect(lineChartSource).toContain('hideOverlap: isCompactChart.value,')
  })

  it('不得覆盖 grid.top —— 温度趋势卡的 45℃ 虚线依赖默认 60', () => {
    expect(lineChartSource).not.toContain('top: isCompactChart.value ? 40')
    // 该耦合的另一半：TempTrendCard 的注释里写明按默认 60 换算（改这里必须同步改那边）
    expect(tempTrendSource).toContain('默认 60')
  })
})