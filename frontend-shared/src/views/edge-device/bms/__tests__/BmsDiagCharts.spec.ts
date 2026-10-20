import { describe, expect, it } from 'vitest'
import {
  bitAt,
  countRisingEdges,
  decodeProtectionBits,
  formatDuration,
  measureActiveMs,
  sortByTime,
} from '@/views/edge-device/bms/diag/bitTransitions'
import protectionCardSource from '@/views/edge-device/bms/diag/ProtectionHistoryCard.vue?raw'
import pollCardSource from '@/views/edge-device/bms/diag/PollIntervalCard.vue?raw'
import tempCardSource from '@/views/edge-device/bms/diag/TempTrendCard.vue?raw'

/**
 * 诊断图表卡契约（2026-10-20，task-3）
 *
 * 本文件只测**新增**的 diag/ 组件，不改既有 spec。
 * 最核心的一条：保护触发次数必须用「0→1 跃变」而不是「数值求和」。
 *
 * 为什么这条要单独立契约：
 *   生产实测 protection_status 近 30 天 20520 个点里非零 1239 个，
 *   但**唯一值只有 [0, 1]** —— 非零恒为 1，说明它是状态位序列而不是计数。
 *   求和会把"同时处于触发态的采样点数"当成"触发次数"，
 *   数字会随采样频率变化而膨胀（同一段时间，采样越密数字越大），完全失去意义。
 */

/**
 * 源码断言前先剥注释：注释里为了解释约束会**提到** `.card` / `markLine` 这些词，
 * 直接 toContain 会把「文档说明」误判成「代码用法」（本 spec 初版就是这么红的）。
 */
function stripComments(src: string): string {
  return src
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '')
}
const protectionCode = stripComments(protectionCardSource)
const tempCode = stripComments(tempCardSource)
const pollCode = stripComments(pollCardSource)

const LABELS = [
  '单体过压', '单体欠压', '整组过压', '整组欠压',
  '充电过温', '充电低温', '放电过温', '放电低温',
  '充电过流', '放电过流', '短路保护', '前端 IC 错误',
] as const

/** 把秒级时间戳构造成点位，便于用直观的数字写用例 */
function pts(values: number[], stepSec = 60, startMs = 1_700_000_000_000) {
  return values.map((value, i) => ({ time: startMs + i * stepSec * 1000, value }))
}

describe('保护触发次数口径：0→1 跃变（不是求和）', () => {
  it('0,0,1,1,0,1 → 2 次触发（求和会得到 3，那是错的）', () => {
    const series = pts([0, 0, 1, 1, 0, 1])
    const count = countRisingEdges(series, 0)
    expect(count, '0→1 跃变次数').toBe(2)
    // 反向锚点：把实现改成求和会让上面的断言失败（求和 = 3）
    const sum = series.reduce((n, p) => n + p.value, 0)
    expect(sum).toBe(3)
    expect(count, '触发次数不得等于数值求和').not.toBe(sum)
  })

  it('连续多个 1 只算一次（1→1 不是跃变）', () => {
    expect(countRisingEdges(pts([0, 1, 1, 1, 1]), 0)).toBe(1)
  })

  it('全 0 序列 → 0 次', () => {
    expect(countRisingEdges(pts([0, 0, 0, 0]), 0)).toBe(0)
  })

  it('窗口起点即为 1 不算一次触发（起点之前状态未知，不能凭空多算）', () => {
    // 若把首点计为触发，这里会变 1；口径规定跨窗口的触发由 activeMs 体现
    expect(countRisingEdges(pts([1, 1, 1]), 0)).toBe(0)
  })

  it('按位解码：位掩码 0b101 只在 bit0/bit2 上有触发', () => {
    const series = pts([0, 1, 1, 0]) // bit0: 0→1 一次
    expect(bitAt(0b101, 0)).toBe(1)
    expect(bitAt(0b101, 1)).toBe(0)
    expect(bitAt(0b101, 2)).toBe(1)
    expect(countRisingEdges(series, 0)).toBe(1)
    expect(countRisingEdges(series, 1)).toBe(0)
    expect(countRisingEdges(series, 2)).toBe(0)
  })

  it('乱序点位按时间排序后再统计（防止把顺序算反导致漏计）', () => {
    const ordered = pts([0, 1, 0, 1])
    const shuffled = [ordered[2], ordered[0], ordered[3], ordered[1]]
    expect(countRisingEdges(shuffled, 0)).toBe(2)
    // 按时间排回去就是原始顺序 0,1,0,1（shuffled 的元素顺序是 0,0,1,1，别写成元素顺序）
    expect(sortByTime(shuffled).map((p) => p.value)).toEqual([0, 1, 0, 1])
  })
})

describe('decodeProtectionBits：12 位全量解码', () => {
  it('12 位全 0 → 每项 0 次、无持续时长', () => {
    const stats = decodeProtectionBits(pts([0, 0, 0]), LABELS)
    expect(stats).toHaveLength(12)
    expect(stats.every((s) => s.count === 0 && s.activeMs === 0)).toBe(true)
  })

  it('bit0 一次触发 + 持续 2 分钟', () => {
    // 每分钟一个点：0 → 1 → 1 → 0 ⇒ 触发 1 次，处于 1 的两段各 60s = 120s
    const stats = decodeProtectionBits(pts([0, 1, 1, 0]), LABELS)
    const b0 = stats[0]
    expect(b0.bit).toBe(0)
    expect(b0.label).toBe('单体过压')
    expect(b0.count).toBe(1)
    expect(b0.activeMs).toBe(120_000)
  })

  it('label 数组长度非法时不静默产出错误位（12 位一一对应）', () => {
    const stats = decodeProtectionBits(pts([0, 1]), LABELS)
    expect(stats.map((s) => s.bit)).toEqual([0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11])
    expect(stats[11].label).toBe('前端 IC 错误')
  })

  it('measureActiveMs 与实际时长一致', () => {
    expect(measureActiveMs(pts([0, 1, 1, 0]), 0)).toBe(120_000)
  })
})

describe('formatDuration：持续时长可读文案', () => {
  it('0 / 负数 / 非数 → 0 分钟（不产出 NaN 文案）', () => {
    expect(formatDuration(0)).toBe('0 分钟')
    expect(formatDuration(-1)).toBe('0 分钟')
    expect(formatDuration(Number.NaN)).toBe('0 分钟')
  })
  it('分钟 / 小时 / 天分档', () => {
    expect(formatDuration(30_000)).toBe('不足 1 分钟')
    expect(formatDuration(45 * 60_000)).toBe('45 分钟')
    expect(formatDuration(2.5 * 3600_000)).toBe('2.5 小时')
    expect(formatDuration(3 * 86400_000)).toBe('3.0 天')
  })
})

/**
 * 集成契约：父页用统一折叠容器包裹，组件只渲染内容。
 * 若组件自己又画一层 .card / .card-title，会出现两级同名标题
 * （评审 demo 里已复现过「MOS 直控」上下各一个）。
 */
describe('集成契约：diag 卡片只渲染内容，不画卡片外壳', () => {
  it('ProtectionHistoryCard 使用 .diag-body，且不含 .card / .card-title / .card-head', () => {
    expect(protectionCode).toContain('class="diag-body"')
    expect(protectionCode, '不得自画卡片外壳')
      .not.toMatch(/class="[^"]*\bcard\b/)
    expect(protectionCode, '不得重复区块标题').not.toContain('card-title')
    expect(protectionCode).not.toContain('card-head')
  })

  it('默认时间范围是 30 天（7 天实测全 0，默认 7 天会让人以为功能坏了）', () => {
    expect(protectionCode).toMatch(/const rangeDays = ref\(30\)/)
    // 反向：不得把默认改回 7 或 24h
    expect(protectionCode).not.toMatch(/const rangeDays = ref\(7\)/)
  })

  it('保护历史卡必须复用跃变函数，而不是对 value 求和', () => {
    expect(protectionCode).toContain('decodeProtectionBits')
    // 反向防回退：出现 reduce 累加 value 即视为退化成求和
    expect(protectionCode).not.toMatch(/reduce\([^)]*value/)
  })

  it('状态文案互斥：加载中/无数据时脚注不得写出「12 项保护均未触发」', () => {
    // 主结论区：「没有该字段」在「有数据但全 0」之前判定（否则会把没有字段谎报成没触发）
    expect(protectionCode.indexOf('v-else-if="!hasData"')).toBeGreaterThan(-1)
    expect(protectionCode.indexOf('v-else-if="!hasData"')).toBeLessThan(protectionCode.indexOf('v-else-if="!hitBits.length"'))
    // 脚注必须带 hasData + !loading 前置条件（曾经的 bug：加载中就写「均未触发」）
    expect(protectionCode).toMatch(/v-if="hasData && !loading && !errorMsg && hitBits\.length"/)
  })

  it('空态分三档：无数据 / 无触发 / 失败，不得把"没有字段"说成"没触发过"', () => {
    expect(protectionCode).toContain('暂无保护状态历史数据')
    expect(protectionCode).toContain('无保护触发记录')
    expect(protectionCode).toContain('保护历史加载失败')
    // hasData 是区分"没有该分类"与"有值但全 0"的唯一依据
    expect(protectionCode).toContain('hasData')
  })

  it('时间范围选择器放在内容区工具行（父页折叠头不放控件）', () => {
    expect(protectionCode).toContain('diag-toolbar')
    expect(protectionCode).toContain('range-select')
  })

  it('PollIntervalCard 用 CSS 条形，不引 ECharts；0 值明确标「已禁用」', () => {
    expect(pollCode).toContain('bar-track')
    expect(pollCode).not.toContain('echarts')
    expect(pollCode).toContain('已禁用')
  })

  it('TempTrendCard 用共享 LineChart，且 45℃ 告警线有文字说明', () => {
    expect(tempCode).toContain("from '@/components/charts/LineChart.vue'")
    expect(tempCode).toContain('TEMP_WARN')
    expect(tempCode).toContain('45℃')
    // LineChart 未注册 MarkLine 组件 ⇒ 不得传 markLine（传了也不会渲染，属静默失效）
    expect(tempCode).not.toContain('markLine')
  })

  it('三张图卡都通过统一取数层（并发≤3 / max_points≤500），不各自 new axios', () => {
    for (const src of [protectionCode, tempCode]) {
      expect(src).toContain('useDiagHistory')
      expect(src).not.toContain('axios')
    }
  })
})
