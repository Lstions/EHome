import { describe, expect, it } from 'vitest'
import source from '@/views/edge-device/bms/BmsDetailPage.vue?raw'

/**
 * BMS 详情页生产级布局契约（designs/bms.png 对齐，2026-08-17）。
 *
 * 设计稿布局：指标卡 → 运行趋势 → 温度探头/MOS/保护 → 底部双列
 * （左：实时数据流；右：指令频率配置 + 受控操作 + 操作历史），所有区块常显。
 * 生产页在其上保留真实功能超集：电芯电压柱图 + 电芯电压历史。
 *
 * 采用 ?raw 源码断言（项目既有模式），锁定结构不回归。
 */
describe('BmsDetailPage production layout (designs/bms.png)', () => {
  it('运行趋势（HistoryChartSection）位于温度探头/MOS 之前', () => {
    const trend = source.indexOf('<HistoryChartSection')
    const temp = source.indexOf('温度探头')
    expect(trend).toBeGreaterThan(-1)
    expect(temp).toBeGreaterThan(-1)
    expect(trend).toBeLessThan(temp)
  })

  it('实时数据流/指令频率配置为常显卡片，不再折叠', () => {
    expect(source).not.toContain('<el-collapse')
    expect(source).not.toContain('activeCollapses')
    expect(source).toContain('实时数据流')
    expect(source).toContain('指令频率配置')
  })

  it('底部为桌面双列：左实时数据流(14) 右指令频率+受控操作(10)，移动端单列堆叠', () => {
    expect(source.match(/<el-col :xs="24" :md="14">/g)?.length).toBe(1)
    expect(source.match(/<el-col :xs="24" :md="10">/g)?.length).toBe(1)
  })

  it('实时数据流卡片带条数徽标，RealtimeDataList 直挂', () => {
    expect(source).toContain('class="realtime-header"')
    expect(source).toContain('{{ realtimeCount }} 条')
    expect(source).toContain('<RealtimeDataList')
  })

  it('右列指令频率配置卡片内嵌 CommandFrequencySection + DeviceControlPanel', () => {
    const col = source.match(/<el-col :xs="24" :md="10">[\s\S]*?<\/el-col>/)?.[0] ?? ''
    expect(col).toContain('CommandFrequencySection')
    expect(col).toContain('DeviceControlPanel')
    expect(col.indexOf('CommandFrequencySection')).toBeLessThan(col.indexOf('DeviceControlPanel'))
  })

  it('温度探头为状态卡片：分档函数 + 状态文案 + 彩色数值', () => {
    expect(source).toContain('function tempLevel')
    expect(source).toContain('function tempStatus')
    expect(source).toContain('偏高')
    expect(source).toContain('过高')
    expect(source).toContain('is-warning')
    expect(source).toContain('is-danger')
  })

  it('MOS 状态保持只读并提示真实控制入口在受控操作', () => {
    expect(source).toContain('mos-hint')
    expect(source).toContain('受控操作')
    // 假开关不得回归：MOS 区域不得出现可交互 toggle
    expect(source).not.toMatch(/MOS状态[\s\S]*?el-switch[\s\S]*?<\/el-card>/)
  })
})
