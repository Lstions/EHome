import { describe, it, expect } from 'vitest'
import src from '../BmsCellVoltageChart.vue?raw'

/**
 * 电芯柱状图：标签降级的「并列极值去重」契约（2026-10-20）
 *
 * 生产实测缺陷：移动端 chartW=292 ⇒ 单柱 18.25px < 36 触发标签降级，
 * 但旧实现用 `value === maxValue || value === minValue` 判定，
 * 并列最值会**同时命中**所有并列项 ⇒ 相邻柱标签互相压字
 * （当轮数据 #4 与 #11 同为 3.227、#8 与 #16 同为 3.234）。
 *
 * 契约：降级时按**首个最值下标**判定，并列只标注第一个。
 * 这里用源码契约（happy-dom 无法断言 canvas 内绘制结果）。
 */
describe('BmsCellVoltageChart 标签降级契约', () => {
  it('不再用「值相等」判定最值（并列会全部命中）', () => {
    // 反向断言：禁止回到 value === maxValue / value === minValue 的写法
    expect(src).not.toMatch(/value\s*===\s*maxValue/)
    expect(src).not.toMatch(/value\s*===\s*minValue/)
  })

  it('按首个最值下标判定（index === maxIndex / minIndex）', () => {
    expect(src).toMatch(/index\s*===\s*maxIndex/)
    expect(src).toMatch(/index\s*===\s*minIndex/)
  })

  it('最值下标取首个出现位置（findIndex，而不是 filter 全收集）', () => {
    expect(src).toMatch(/const\s+maxIndex\s*=\s*values\.findIndex/)
    expect(src).toMatch(/const\s+minIndex\s*=\s*values\.findIndex/)
  })

  it('formatter 传入 dataIndex（决定当前柱是哪一个）', () => {
    expect(src).toMatch(/p\.dataIndex/)
  })
})
