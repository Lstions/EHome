import { describe, expect, it } from 'vitest'
import cellChartSource from '../BmsCellVoltageChart.vue?raw'
import bmsDetailSource from '../BmsDetailPage.vue?raw'

/**
 * task-4：电芯电压分布卡「窄屏 112px 可读性」契约（2026-10-20）。
 *
 * 背景：393px 下整卡 477px（图区 300px）占掉半屏，方案 B 要求压到 ≈190-210px。
 * 分工：父页（BmsDetailPage.vue，Lead 维护）负责卡头/图例/脚注与图区高度 112px，
 *      本组件负责「图在 112px 高 × 393px 宽下仍然可读」：
 *        · Y 轴固定 3 个 tick、去轴名、压缩 grid/padding；
 *        · 柱顶标签继续走既有 shouldShowCellLabel 降级（**函数签名不得改**，有 spec 锚定）；
 *        · tooltip 保留（全量精确值的唯一兜底通道，不得为了省高度删掉）。
 *
 * 本 spec 只做**源码契约**断言（与 BmsCellVoltageLabelDegrade / BmsMobileResponsive
 * 同一模式）：ECharts 是 Canvas 渲染，happy-dom 无法断言绘制结果；
 * 真实浏览器证据见 .logs/ui-fix/review-shots/（112px 截图 + 桌面改前改后像素对比）。
 *
 * 两层护栏：
 *   ① 正向：紧凑布局与父页窄屏接入点存在；
 *   ② 反向防回退：不得把桌面配置改成移动端配置、不得删 tooltip/labelLayout、
 *      不得用 ellipsis 吞数值、12 个保护名称不得从页面消失。
 */

const BMS_CHART_TAG = bmsDetailSource.match(/<BmsCellVoltageChart[\s\S]*?\/>/)?.[0] ?? ''
const MOBILE_BLOCK_START = bmsDetailSource.indexOf('@media (max-width: 768px)')
const MOBILE_BLOCK = MOBILE_BLOCK_START >= 0 ? bmsDetailSource.slice(MOBILE_BLOCK_START) : ''

describe('BmsCellVoltageChart 窄屏 112px 紧凑布局（容器感知）', () => {
  it('用容器宽/高推导紧凑模式（宽 <768 或高 <180），不新增 window 监听', () => {
    expect(cellChartSource).toMatch(/const isCompact = computed/)
    expect(cellChartSource).toContain('chartWidth.value < 768')
    expect(cellChartSource).toContain('chartHeight.value > 0 && chartHeight.value < 180')
    // 既有契约（BmsMobileResponsive.spec）：重绘必须留在 ResizeObserver 内，组件不得挂 window 监听
    expect(cellChartSource).not.toContain('addEventListener')
    expect(cellChartSource).toMatch(/new ResizeObserver\(\(\) => \{[\s\S]*?chartWidth\.value = chartRef\.value\.clientWidth[\s\S]*?updateChart\(\)/)
  })

  it('容器高度由 ResizeObserver 维护（父页 300→112 时能触发紧凑重绘）', () => {
    expect(cellChartSource).toContain('const chartHeight = ref(0)')
    expect(cellChartSource).toContain('chartHeight.value = chartRef.value.clientHeight')
    // 首帧前先记录尺寸（否则 112px 首帧会先按桌面布局画一次）
    expect(cellChartSource).toMatch(/if \(chartRef\.value\) \{\s*chartWidth\.value = chartRef\.value\.clientWidth\s*chartHeight\.value = chartRef\.value\.clientHeight/)
  })

  it('紧凑态 Y 轴固定 3 个 tick（splitNumber 2 + 显式 interval）', () => {
    expect(cellChartSource).toMatch(/compact \? \{ splitNumber: 2, interval: \(max - min\) \/ 2 \} : \{\}/)
  })

  it('紧凑态去轴名，桌面仍保留「电压(V)」', () => {
    expect(cellChartSource).toMatch(/name: compact \? '' : '电压\(V\)'/)
  })

  it('紧凑态压缩 grid/留白，桌面百分比 grid 原样保留', () => {
    expect(cellChartSource).toContain('left: 2, right: 2, top: 16, bottom: 2, containLabel: true')
    expect(cellChartSource).toContain("left: '3%', right: '4%', bottom: '3%', top: '10%', containLabel: true")
  })

  it('紧凑态柱顶标签字号降一档，桌面字号 10 不变（逐像素不回归的前提）', () => {
    expect(cellChartSource).toMatch(/fontSize: compact \? 9 : 10/)
    expect(cellChartSource).toContain('fontSize: 11') // 桌面 x 轴字号
  })

  it('紧凑态只留首/尾电芯刻度（16 个「#N」在 ~20px/柱下必然压字）', () => {
    expect(cellChartSource).toMatch(/i === 0 \|\| i === labels\.length - 1 \? v : ''/)
    expect(cellChartSource).toContain('interval: 0')
  })

  it('柱顶标签继续走 shouldShowCellLabel 降级，函数签名与 36px 阈值不得改', () => {
    expect(cellChartSource).toContain('const LABEL_DEGRADE_THRESHOLD = 36')
    expect(cellChartSource).toMatch(/function shouldShowCellLabel\(\s*value: number,\s*cellWidthPx: number,\s*index: number,\s*maxIndex: number,\s*minIndex: number,\s*\): boolean/)
    expect(cellChartSource).toMatch(/shouldShowCellLabel\(v, cellWidthPx, p\.dataIndex, maxIndex, minIndex\)/)
    expect(cellChartSource).toMatch(/const maxIndex = values\.findIndex/)
    expect(cellChartSource).toMatch(/const minIndex = values\.findIndex/)
  })

  it('tooltip 保留（全量精确值兜底通道，紧凑态也不得删）', () => {
    expect(cellChartSource).toContain("trigger: 'axis' as const")
    expect(cellChartSource).toContain('confine: true')
    expect(cellChartSource).toMatch(/电压: <b>\$\{typeof p\.value === 'number' \? p\.value\.toFixed\(3\)/)
  })

  it('紧凑态启用 labelLayout.hideOverlap 防相邻柱压字（最高/最低可能相邻）', () => {
    expect(cellChartSource).toMatch(/labelLayout: \{ hideOverlap: true \}/)
    // 只允许在紧凑分支启用：桌面 1102px 宽标签本就不重叠，加 hideOverlap 会改变桌面绘制
    expect(cellChartSource).toMatch(/\.\.\.\(compact \? \{ labelLayout: \{ hideOverlap: true \} \} : \{\}\)/)
  })

  it('按需引入的 ECharts 必须注册 LabelLayout，否则 hideOverlap 被静默忽略', () => {
    /* 2026-10-20 探针实测：未注册 LabelLayout 时，相邻极值标签仍叠成「3.499501」；
       注册后 ECharts 自动隐藏冲突标签（旧版 3.499/3.501 两个标签叠字 → 新版只留 3.501）。 */
    expect(cellChartSource).toContain("import { LabelLayout } from 'echarts/features'")
    expect(cellChartSource).toMatch(/echarts\.use\(\[[\s\S]*?LabelLayout[\s\S]*?\]\)/)
  })

  it('紧凑态收起「下限/上限」文字标注，但两条阈值虚线（3.0/3.6）必须保留', () => {
    expect(cellChartSource).toContain("label: compact ? { show: false } : { formatter: '下限', color: surface.regular }")
    expect(cellChartSource).toContain("label: compact ? { show: false } : { formatter: '上限', color: surface.regular }")
    expect(cellChartSource).toContain('yAxis: 3.0')
    expect(cellChartSource).toContain('yAxis: 3.6')
  })

  it('反向防回退：不得用 ellipsis/nowrap 截断电芯数值', () => {
    expect(cellChartSource).not.toMatch(/text-overflow:\s*ellipsis/)
    expect(cellChartSource).not.toContain('overflow: hidden')
  })
})

describe('BmsDetailPage 电芯电压分布卡窄屏接入点（只读断言，不改该文件）', () => {
  it('桌面图区固定 300px（桌面逐像素不变量）', () => {
    expect(bmsDetailSource).toMatch(/\.cell-chart-host \{ height: 300px; \}/)
  })

  it('窄屏 media query 里图区收到 112px（空态占位同高，避免失败态塌陷）', () => {
    expect(MOBILE_BLOCK_START, '未找到 @media (max-width: 768px)，移动端规则会落空').toBeGreaterThan(0)
    // 父页实现：.cell-chart-host, .cell-chart-empty { height: 112px; } —— 空态与有数据同高
    expect(MOBILE_BLOCK).toMatch(/\.cell-chart-host[^{]*\{[^}]*height: 112px/)
    expect(MOBILE_BLOCK).toMatch(/\.cell-chart-empty[^{]*\{[^}]*height: 112px/)
  })

  it('BmsCellVoltageChart 的 height prop 在窄屏传 112px（否则 300px 图会溢出 112px 容器）', () => {
    expect(BMS_CHART_TAG, '未找到 <BmsCellVoltageChart ... /> 标签').not.toBe('')
    expect(BMS_CHART_TAG).toContain('112px')
    // 桌面高度仍是 300px（父页传 300px / 组件默认 280px 均不得被窄屏值顶掉）
    expect(BMS_CHART_TAG).toMatch(/300px|height="300px"/)
  })

  it('「最高/最低/压差」摘要信息完整保留（不因压缩卡头而删数据）', () => {
    expect(bmsDetailSource).toContain('最高')
    expect(bmsDetailSource).toContain('最低')
    expect(bmsDetailSource).toContain('压差')
  })

  it('图例三档与阈值文本保留（窄屏压成一行不等于删信息）', () => {
    for (const txt of ['正常', '偏高', '偏低', '3.000V', '3.600V']) {
      expect(bmsDetailSource).toContain(txt)
    }
    expect(bmsDetailSource).toContain('swatch-normal')
    expect(bmsDetailSource).toContain('swatch-high')
    expect(bmsDetailSource).toContain('swatch-danger')
  })

  it('12 个保护名称必须全部仍在页面 DOM（保护卡 3 列网格化的前提）', () => {
    const names = ['单体过压', '单体欠压', '整组过压', '整组欠压',
      '充电过温', '充电低温', '放电过温', '放电低温',
      '充电过流', '放电过流', '短路保护', '前端 IC 错误']
    for (const name of names) {
      expect(bmsDetailSource, '保护名称被删：' + name).toContain(name)
    }
    // 名称来自 PROTECTION_BITS 且由 v-for 渲染 ⇒ 在 DOM 里
    expect(bmsDetailSource).toContain('v-for="p in protectionBits"')
    // 移动端只收起 bit 号，名称不截断
    expect(MOBILE_BLOCK).toMatch(/\.protect-bit \{ display: none; \}/)
  })

  it('空态文案保留（fail-open：采不到数据不能让用户以为一切正常）', () => {
    expect(bmsDetailSource).toContain('暂无单体电压数据')
    expect(bmsDetailSource).toContain('暂无 protection_status 数据')
  })
})
