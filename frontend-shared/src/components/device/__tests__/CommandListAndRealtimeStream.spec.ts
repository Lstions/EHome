import { describe, expect, it } from 'vitest'
import commandListSource from '@/components/device/CommandList.vue?raw'
import realtimeListSource from '@/components/data/RealtimeDataList.vue?raw'
import bmsDetailSource from '@/views/edge-device/bms/BmsDetailPage.vue?raw'

/**
 * 指令频率配置 + 实时数据流重设计源码契约测试（2026-08-09）。
 *
 * 仅断言 CSS/模板结构契约（交互行为由 CommandListRedesign.spec.ts /
 * RealtimeDataList.spec.ts 覆盖——规范 3.7.1）。目的：任何人 rebase
 * 把固定宽度裁剪布局/固定行高/嵌套卡片加回来，测试当场红。
 */

describe('CommandList 容器感知契约', () => {
  it('指令行不再使用固定宽度 + nowrap 裁剪布局', () => {
    // 历史缺陷：.cmd-info width:180px + .cmd-controls width:200px + nowrap
    // 在 390px 视口（可用 ~310px）下数学上必然裁剪
    expect(commandListSource).not.toContain('width: 180px')
    expect(commandListSource).not.toContain('width: 200px')
    expect(commandListSource).not.toContain('flex-wrap: nowrap')
    // 指令行容器禁止裁剪（描述区单行 ellipsis 的 overflow:hidden 是合法契约，不在禁止范围）
    expect(commandListSource).not.toMatch(/\.command-item\s*\{[^}]*overflow/s)
  })

  it('指令行使用 flex-wrap 降级契约', () => {
    expect(commandListSource).toMatch(/\.command-item\s*\{[^}]*flex-wrap:\s*wrap/s)
  })

  it('≤768px 切两段式纵向堆叠', () => {
    expect(commandListSource).toMatch(/@media\s*\(max-width:\s*768px\)[^]*flex-direction:\s*column/)
    // column 方向下 .cmd-desc 的 flex-basis:160px 会错误作用到高度撑出空白，必须重置
    expect(commandListSource).toMatch(/@media\s*\(max-width:\s*768px\)[^]*\.cmd-desc\s*\{[^}]*flex:\s*none/s)
  })

  it('状态双通道：禁用不用 danger，启用态有文字表达', () => {
    // 状态 tag type 只在 success/info 间切换（规范 4.2.1：OFF/停止不得用 danger）
    expect(commandListSource).toContain("isEnabled(cmd.id) ? 'success' : 'info'")
    expect(commandListSource).toContain('已禁用')
    expect(commandListSource).toContain('轮询中')
  })

  it('加载失败不再静默吞掉，有显性错误与重试路径', () => {
    expect(commandListSource).toContain('loadFailed')
    expect(commandListSource).toContain('轮询指令加载失败')
    expect(commandListSource).toContain('重试')
  })

  it('保存门禁：dirty 判定 + 禁用原因说明', () => {
    expect(commandListSource).toContain('dirty')
    expect(commandListSource).toContain('当前配置与节点一致')
  })
})

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

describe('BmsDetailPage 接线契约', () => {
  it('折叠面板内 CommandFrequencySection 使用 embedded 消除双层嵌套', () => {
    expect(bmsDetailSource).toMatch(/<CommandFrequencySection[^>]*embedded/)
  })
})
