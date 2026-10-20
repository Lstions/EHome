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
    /*
     * ⚠ 两条经验，都来自本用例的真实误红：
     *   ① 正则不要写死参数列表 —— 2026-10-20 给 shouldShowCellLabel 增加了
     *      (index, maxIndex, minIndex) 用于并列极值去重；
     *   ② 不要在 formatter: 后贪婪跨段匹配 —— 源码里 tooltip 的 formatter 出现在
     *      series.label 之前，从第一个 formatter 起匹配会跨过 } 边界而失配。
     *      直接锚定系列标签 formatter 内部对 shouldShowCellLabel 的调用。
     */
    expect(cellChartSource).toMatch(/shouldShowCellLabel\([^)]*\)\s*\?\s*v\.toFixed\(3\)\s*:\s*''/)
    expect(cellChartSource).toContain("trigger: 'axis' as const")
    expect(cellChartSource).toContain('V</b>`')
  })

  /**
   * 并列极值去重（2026-10-20 新增缺陷）：
   * 生产实测 #4 与 #11 同为 3.227(min)、#8 与 #16 同为 3.234(max)。
   * 降级模式下若按"值相等"判定，并列项全部返回标签 ⇒ 相邻柱标签压字。
   * 契约：必须按首个下标判定，且把 dataIndex 传进判定函数。
   */
  it('并列极值只标注第一个（防相邻柱标签压字）', () => {
    expect(cellChartSource).toMatch(/const maxIndex = values\.findIndex/)
    expect(cellChartSource).toMatch(/const minIndex = values\.findIndex/)
    expect(cellChartSource).toMatch(/shouldShowCellLabel\(v, cellWidthPx, p\.dataIndex, maxIndex, minIndex\)/)
    // 反向断言：不得回退成"值相等即标注"
    expect(cellChartSource).not.toMatch(/value === maxValue/)
    expect(cellChartSource).not.toMatch(/value === minValue/)
  })

  it('keeps min/max mark lines and theme-driven colors intact', () => {
    expect(cellChartSource).toContain("{ formatter: '下限', color: surface.regular }")
    expect(cellChartSource).toContain("{ formatter: '上限', color: surface.regular }")
    expect(cellChartSource).toContain('getThemeColors()')
  })
})

describe('BmsDetailPage 卡片标题换行行为（重设计版）', () => {
  it('卡片头统一 flex-wrap，长摘要换行而不是挤掉标题', () => {
    // 重设计后用统一 .card-head 取代旧版每卡一套 .cell-voltage-header/.protection-header：
    // 一条共享规则而非两份重复声明，语义等价（标题与右侧摘要可换行）。
    expect(bmsDetailSource).toMatch(/\.card-head \{[^}]*display:\s*flex[^}]*flex-wrap:\s*wrap/)
  })

  it('.head-right 自身也可换行（摘要较长时不撑破卡片）', () => {
    expect(bmsDetailSource).toMatch(/\.head-right \{[^}]*flex-wrap:\s*wrap/)
  })

  it('保留完整的串数/最高/最低/压差摘要内容', () => {
    expect(bmsDetailSource).toContain('串 · 单位 V')
    expect(bmsDetailSource).toContain('最高')
    expect(bmsDetailSource).toContain('最低')
    expect(bmsDetailSource).toContain('压差')
  })
})

describe('BmsDetailPage 窄屏断点（重设计版）', () => {
  it('用流式 flex-basis + media query 堆叠，而非 el-col 栅格断点', () => {
    // 旧版靠 el-col :xs/:sm/:md 控制堆叠；重设计改流式后由 flex-basis/min-width
    // 自动换行，窄屏断点收窄大卡基准宽度让堆叠更早发生。
    expect(bmsDetailSource).not.toMatch(/<el-col[^>]*:md="1[04]"/)
    expect(bmsDetailSource).toMatch(/@media \(max-width: 1200px\)/)
    expect(bmsDetailSource).toMatch(/@media \(max-width: 768px\)/)
  })

  it('保留粗指针 44px 触摸目标（移动端可达性）', () => {
    expect(bmsDetailSource).toMatch(/@media \(pointer: coarse\)[\s\S]*?min-height:\s*44px/)
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
describe('BmsDetailPage 轮询指令行窄屏重排（762 → 522px）', () => {
  it('桌面仍是 flex 单段布局，窄屏才改成 2×2 网格（区域名 ctl 跨两行）', () => {
    // 桌面规则必须原样保留：一旦被网格规则顶掉，1440 下输入框会掉到第二行
    expect(bmsDetailSource).toMatch(/\.poll-row \{ display: flex; align-items: center; gap: 12px;/)
    expect(bmsDetailSource).toMatch(/grid-template-areas: 'name ctl' 'desc desc'/)
    // ctl 只写在第一行 → 控件列不跨行，行高会被 44px 输入框顶成两段（实测 75px）
    // 这里锁住「控件跨行 + 描述独占整行」的组合意图
    expect(bmsDetailSource).toMatch(/\.poll-controls \{ grid-area: ctl; width: auto; gap: 6px; \}/)
  })

  it('输入框收窄到 104px 但仍可输入任意毫秒（0–60000，无 precision 限制）', () => {
    expect(bmsDetailSource).toMatch(/\.poll-interval \{ flex: 0 0 auto; width: 104px; \}/)
    expect(bmsDetailSource).toContain(':min="0"')
    expect(bmsDetailSource).toContain(':max="60000"')
    // 有 precision 就只能填整千，用户明确要求任意毫秒
    expect(bmsDetailSource).not.toMatch(/<el-input-number[\s\S]{0,400}?precision=/)
  })

  it('窄屏只隐藏与开关同义的「已禁用」标签，带告警色的「超限」标签保留', () => {
    expect(bmsDetailSource).toMatch(/\.poll-name-line :deep\(\.el-tag--info\) \{ display: none; \}/)
    expect(bmsDetailSource).toContain('type="warning" size="small" effect="dark">超限</el-tag>')
  })

  it('卡头机制说明包进 .note-limit-detail 只在桌面显示，约束另有两个可见通道', () => {
    expect(bmsDetailSource).toContain('<span class="note-limit-detail">')
    expect(bmsDetailSource).toMatch(/\.poll-card \.card-note \.note-limit-detail \{ display: none; \}/)
    // 通道一：卡头配额标签；通道二：超限/占满告警条
    expect(bmsDetailSource).toContain('已启用 {{ enabledCount }}/{{ pollLimitValue }}')
    expect(bmsDetailSource).toContain('超过上限 {{ pollLimitValue }} 条，保存会被拒绝')
  })
})
