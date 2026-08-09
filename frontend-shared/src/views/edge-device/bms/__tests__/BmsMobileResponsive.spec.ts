import { describe, expect, it } from 'vitest'
import { ref, computed } from 'vue'
import bmsDetailSource from '@/views/edge-device/bms/BmsDetailPage.vue?raw'
import cellChartSource from '@/views/edge-device/bms/BmsCellVoltageChart.vue?raw'
import deviceInfoCardSource from '@/views/edge-device/shared/DeviceInfoCard.vue?raw'
import deviceHeaderSource from '@/views/edge-device/shared/DeviceHeader.vue?raw'

/**
 * BMS 详情页「容器感知 + 断点 + 页头」修复源码契约测试（2026-08-09）。
 *
 * 采用项目「响应式逻辑轻量测试模式」：不 mount，用 ref+computed 复现容器宽度降级
 * 判定逻辑，再以 ?raw 源码断言模板/样式接入点。覆盖 5 项已确认问题：
 *  1) 电芯电压柱图 16 个柱顶数值重叠 → 容器感知标签降级：clientWidth/cellCount < 36px 时
 *     仅保留 越界节(<3.0/>3.6)/最高/最低 标签，全量精确数值走 axis tooltip
 *  2) 电芯电压/保护状态 卡片 header 标题被长摘要挤成逐字断行 → 标题 nowrap + flex-shrink:0，
 *     摘要空间不足时换到第二行
 *  3) 温度探头/MOS 状态 el-col 恒 2 列挤压 → 补 :xs="24" 断点，≤768px 单列
 *  4) DeviceInfoCard 移动端 label 列 92px 拆字 → minmax(max-content, 106px) + nowrap
 *  5) DeviceHeader 移动端 4 按钮挤压标题 → 编辑/同步到HA/删除 收进 el-dropdown 溢出下拉，
 *     保留「刷新数据」主按钮
 */

// ---------- 轻量复现 BmsCellVoltageChart 容器宽度降级判定逻辑 ----------
const LABEL_DEGRADE_THRESHOLD = 36

/**
 * 与组件内 shouldShowCellLabel 保持一致的降级判定（不 mount，直接复现）：
 * - 单柱可用宽度 ≥ 阈值：全量显示（v > 0）
 * - 单柱可用宽度 < 阈值：仅显示越界节（<3.0 或 >3.6，与 getBarColors 阈值一致）与最高/最低节
 */
function shouldShowCellLabel(value: number, cellWidthPx: number, maxValue: number, minValue: number): boolean {
  if (cellWidthPx >= LABEL_DEGRADE_THRESHOLD) return value > 0
  if (value <= 0) return false
  if (value < 3.0 || value > 3.6) return true
  return value === maxValue || value === minValue
}

/** 复现组件内 cellWidthPx = chartWidth / cellCount 的 computed 推导 */
function makeCellWidthPx(chartWidth: number, cellCount: number) {
  const width = ref(chartWidth)
  const count = ref(cellCount)
  return computed(() => (count.value > 0 ? width.value / count.value : 0))
}

describe('BmsCellVoltageChart container-aware label degradation (P0-1)', () => {
  it('degrades below threshold: 390px/16 ≈ 24px per cell → only extreme/out-of-range cells keep labels', () => {
    const cellWidthPx = makeCellWidthPx(390, 16).value
    expect(cellWidthPx).toBeLessThan(LABEL_DEGRADE_THRESHOLD)
    // 越界节（<3.0 / >3.6）保留标签
    expect(shouldShowCellLabel(2.85, cellWidthPx, 3.55, 3.05)).toBe(true)
    expect(shouldShowCellLabel(3.61, cellWidthPx, 3.55, 3.05)).toBe(true)
    // 最高/最低节（且在正常范围内）保留标签
    expect(shouldShowCellLabel(3.55, cellWidthPx, 3.55, 3.05)).toBe(true)
    expect(shouldShowCellLabel(3.05, cellWidthPx, 3.55, 3.05)).toBe(true)
    // 普通中间节降级为空标签
    expect(shouldShowCellLabel(3.3, cellWidthPx, 3.55, 3.05)).toBe(false)
    // 无效值（0 占位）始终不显示
    expect(shouldShowCellLabel(0, cellWidthPx, 3.55, 3.05)).toBe(false)
  })

  it('keeps full labels when space is sufficient: 800px/16 ≈ 50px per cell', () => {
    const cellWidthPx = makeCellWidthPx(800, 16).value
    expect(cellWidthPx).toBeGreaterThanOrEqual(LABEL_DEGRADE_THRESHOLD)
    expect(shouldShowCellLabel(3.3, cellWidthPx, 3.55, 3.05)).toBe(true)
    expect(shouldShowCellLabel(3.55, cellWidthPx, 3.55, 3.05)).toBe(true)
  })

  it('is generic across cell counts: 390px/8 ≈ 49px per cell → full labels (no hardcoded media query)', () => {
    const cellWidthPx = makeCellWidthPx(390, 8).value
    expect(cellWidthPx).toBeGreaterThanOrEqual(LABEL_DEGRADE_THRESHOLD)
    expect(shouldShowCellLabel(3.3, cellWidthPx, 3.55, 3.05)).toBe(true)
  })

  it('measures the container width via chartRef.clientWidth with a 36px threshold', () => {
    expect(cellChartSource).toContain('const chartWidth = ref(0)')
    expect(cellChartSource).toContain('const LABEL_DEGRADE_THRESHOLD = 36')
    expect(cellChartSource).toContain('chartRef.value.clientWidth')
  })

  it('re-renders inside the existing ResizeObserver instead of adding a window listener', () => {
    expect(cellChartSource).toMatch(/new ResizeObserver\(\(\) => \{[\s\S]*?chartWidth\.value = chartRef\.value\.clientWidth[\s\S]*?updateChart\(\)/)
    expect(cellChartSource).toContain('chartInstance?.resize()')
    expect(cellChartSource).not.toContain('addEventListener')
  })

  it('wires shouldShowCellLabel into series.label and keeps the axis tooltip for full values', () => {
    expect(cellChartSource).toMatch(/formatter: \(p: any\) => \{[\s\S]*?shouldShowCellLabel\([\s\S]*?\.toFixed\(3\) ?: ''/)
    expect(cellChartSource).toContain("trigger: 'axis' as const")
    expect(cellChartSource).toContain('V</b>`')
  })

  it('keeps min/max mark lines and theme-driven colors intact', () => {
    expect(cellChartSource).toContain("{ formatter: '下限', color: surface.regular }")
    expect(cellChartSource).toContain("{ formatter: '上限', color: surface.regular }")
    expect(cellChartSource).toContain('getThemeColors()')
  })
})

describe('BmsDetailPage card headers no-wrap title (P1-2)', () => {
  it('marks both 电芯电压/保护状态 titles nowrap + flex-shrink 0', () => {
    // 选择器可能分组书写（.cell-voltage-title, .protection-title { ... }），用 [^{]* 容忍
    expect(bmsDetailSource).toMatch(/\.cell-voltage-title[^{]*\{[^}]*white-space:\s*nowrap[^}]*flex-shrink:\s*0[^}]*\}/)
    expect(bmsDetailSource).toMatch(/\.protection-title[^{]*\{[^}]*white-space:\s*nowrap[^}]*flex-shrink:\s*0[^}]*\}/)
  })

  it('lets the summary tag wrap to a second line instead of squeezing the title', () => {
    expect(bmsDetailSource).toMatch(/\.cell-voltage-header[^{]*\{[^}]*flex-wrap:\s*wrap[^}]*\}/)
    expect(bmsDetailSource).toMatch(/\.protection-header[^{]*\{[^}]*flex-wrap:\s*wrap[^}]*\}/)
  })

  it('keeps the full 16节/最低/最高/压差 summary content', () => {
    expect(bmsDetailSource).toContain('节 · 最低')
    expect(bmsDetailSource).toContain('· 最高')
    expect(bmsDetailSource).toContain('· 压差')
  })
})

describe('BmsDetailPage temperature/MOS breakpoints (P1-3)', () => {
  it('adds :xs="24" :sm="12" so 温度探头/MOS 状态 stack to one column below 768px', () => {
    expect(bmsDetailSource.match(/<el-col :xs="24" :sm="12">/g)?.length).toBe(2)
  })

  it('keeps the 4 metric cards on their own mobile 2-col breakpoint untouched', () => {
    expect(bmsDetailSource.match(/<el-col :xs="12" :sm="12" :md="6">/g)?.length).toBe(4)
  })
})

describe('DeviceInfoCard mobile label column (P1-4)', () => {
  it('no longer hardcodes the 92px label column', () => {
    expect(deviceInfoCardSource).not.toContain('92px')
  })

  it('derives the label column from the longest label via minmax(max-content, 106px)', () => {
    expect(deviceInfoCardSource).toContain('grid-template-columns: minmax(max-content, 106px) minmax(0, 1fr)')
  })

  it('keeps labels on one line (no 拆字) with white-space nowrap', () => {
    expect(deviceInfoCardSource).toMatch(/\.mobile-info-label\s*\{[^}]*white-space:\s*nowrap[^}]*\}/)
  })

  it('keeps all standard fields in both mobile and desktop layouts', () => {
    const labels = ['设备名称', '设备类型', '通信协议', '硬件类型', '硬件ID', '健康状态', '最后数据时间']
    for (const label of labels) {
      // 移动端 mobile-info-label 与桌面端 el-descriptions-item 各出现一次
      const occurrences = deviceInfoCardSource.match(new RegExp(label, 'g'))
      expect(occurrences?.length, label).toBeGreaterThanOrEqual(2)
    }
  })
})

describe('DeviceHeader mobile overflow dropdown (P1-5)', () => {
  it('uses useResponsive isMobile to switch between full action row and overflow dropdown', () => {
    expect(deviceHeaderSource).toContain("import { useResponsive } from '@/composables/useResponsive'")
    expect(deviceHeaderSource).toContain('const { isMobile } = useResponsive()')
    expect(deviceHeaderSource).toMatch(/<el-dropdown v-if="isMobile"[\s\S]*?popper-class="device-header-more-popover"/)
    expect(deviceHeaderSource).toContain('MoreFilled')
  })

  it('moves 编辑/同步到HA/删除 into the dropdown menu on mobile', () => {
    expect(deviceHeaderSource).toMatch(/<el-dropdown-item command="edit"[\s\S]*?编辑<\/el-dropdown-item>/)
    expect(deviceHeaderSource).toMatch(/<el-dropdown-item command="syncToHA"[\s\S]*?同步到HA[\s\S]*?<\/el-dropdown-item>/)
    expect(deviceHeaderSource).toMatch(/<el-dropdown-item command="delete"[\s\S]*?删除[\s\S]*?<\/el-dropdown-item>/)
    // 刷新数据是移动端保留的主按钮，不进下拉
    expect(deviceHeaderSource.match(/<el-dropdown-item command="refresh"/g)).toBeNull()
  })

  it('keeps desktop full action row and the always-visible refresh button', () => {
    expect(deviceHeaderSource).toContain('<template v-if="!isMobile">')
    expect(deviceHeaderSource).toContain('type="primary" :icon="Refresh"')
    // 刷新按钮不在 !isMobile 分支内，移动端仍可见
    const desktopGroup = deviceHeaderSource.match(/<template v-if="!isMobile">[\s\S]*?<\/template>/)?.[0] ?? ''
    expect(desktopGroup).not.toContain('刷新数据')
  })

  it('routes dropdown commands to the same edit/sync/delete handlers', () => {
    expect(deviceHeaderSource).toContain('function handleMoreCommand(command: string)')
    expect(deviceHeaderSource).toContain("if (command === 'edit') editDialogVisible.value = true")
    expect(deviceHeaderSource).toContain("else if (command === 'syncToHA') emit('syncToHA')")
    expect(deviceHeaderSource).toContain("else if (command === 'delete') deleteDialogVisible.value = true")
  })

  it('limits the dropdown popper width via a global popper-class (popover 不受 92vw .el-dialog 兜底)', () => {
    expect(deviceHeaderSource).toContain('popper-class="device-header-more-popover"')
    expect(deviceHeaderSource).toMatch(/:global\(\.device-header-more-popover\)\s*\{[^}]*max-width:\s*calc\(100vw - 24px\)[^}]*\}/)
  })
})
