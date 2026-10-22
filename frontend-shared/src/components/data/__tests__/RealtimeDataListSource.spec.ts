import { describe, expect, it } from 'vitest'
import realtimeListSource from '@/components/data/RealtimeDataList.vue?raw'

/**
 * 实时数据流行的源码结构契约（原 CommandListAndRealtimeStream.spec 的实时部分；
 * 指令频率部分已迁到 views/edge-device/shared/__tests__/CommandFrequencyShared.spec.ts）。
 *
 * 交互行为由 RealtimeDataList.spec.ts 覆盖（规范 3.7.1）；这里只锁结构：
 * 任何人 rebase 把固定行高虚拟滚动 / min-height 撑高 / 假空态加回来，测试当场红。
 */
describe('RealtimeDataList 行高与空态契约', () => {
  it('不再使用固定行高虚拟滚动（多行帧不被裁剪）', () => {
    expect(realtimeListSource).not.toContain('RecycleScroller')
    expect(realtimeListSource).not.toContain(':item-size="64"')
    expect(realtimeListSource).toContain('.plain-list')
    expect(realtimeListSource).toContain('white-space: pre-wrap')
  })

  it('容器高度随内容增长：无 min-height 撑空白框', () => {
    // 历史缺陷：min-height:200px 使 1 条数据也撑出大空白框
    expect(realtimeListSource).not.toMatch(/min-height:\s*200px/)
    expect(realtimeListSource).not.toMatch(/min-height:\s*240px/)
  })

  it('空态使用共享 EmptyState 而非各页散落的 el-empty', () => {
    expect(realtimeListSource).toContain("from '@/components/common/EmptyState.vue'")
    expect(realtimeListSource).toContain('kind="initial"')
  })

  it('格式化结果为空时有中性占位兜底', () => {
    expect(realtimeListSource).toContain('(无数据字段)')
  })
})
