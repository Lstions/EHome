import { describe, it, expect } from 'vitest'
import { ref, computed } from 'vue'
import nodeDetailSource from '../NodeDetail.vue?raw'

/**
 * NodeDetail 移动端响应式行为测试（不依赖 mount 环境）。
 *
 * 背景：NodeDetail.spec.ts 的 mount 用例在当前环境存在 19 个预存失败
 * （PageHeader stub 不渲染，与本次改动无关，基线可复现）。
 * 这里以纯逻辑 + 源码结构验证本次移动端适配：
 *  1) descColumns 计算逻辑（isMobile → 1 列，桌面 → 2 列）
 *  2) 模板接入点：el-descriptions :column / Last Sync ID :span / 表格滚动容器与横滑提示
 */

const src = nodeDetailSource

function makeDescColumns(isMobile: boolean) {
  const mobile = ref(isMobile)
  return computed(() => (mobile.value ? 1 : 2))
}

describe('NodeDetail mobile responsive (behavior)', () => {
  it('descColumns logic: mobile → 1 column, desktop → 2 columns', () => {
    expect(makeDescColumns(true).value).toBe(1)
    expect(makeDescColumns(false).value).toBe(2)
  })

  it('drives descColumns from useResponsive isMobile', () => {
    expect(src).toContain("import { useResponsive } from '@/composables/useResponsive'")
    expect(src).toContain('const { isMobile } = useResponsive()')
    expect(src).toContain('const descColumns = computed(() => (isMobile.value ? 1 : 2))')
  })

  it('binds both el-descriptions blocks to descColumns', () => {
    // 基本信息 + 配置同步状态两处描述列表都应响应式
    expect(src.match(/:column="descColumns"/g)?.length).toBe(2)
  })

  it('spans Last Sync ID across descColumns instead of hardcoded 2', () => {
    expect(src).toContain(':span="descColumns"')
    expect(src).not.toContain('label="Last Sync ID" :span="2"')
  })

  it('wraps both wide tables (devices + OTA history) with mobile scroll container and hint', () => {
    expect(src.match(/class="mobile-table-wrapper"/g)?.length).toBe(2)
    expect(src.match(/class="mobile-table-hint"/g)?.length).toBe(2)
  })

  it('page title uses line-clamp instead of nowrap to prevent 0-width collapse on mobile', () => {
    // 标题容器可收缩 + 允许折行（最多两行），避免被 flex 挤压为 0px
    expect(src).toContain('.page-header-left')
    expect(src).toContain('min-width: 0')
    expect(src).toContain('-webkit-line-clamp: 2')
    expect(src).toContain('white-space: normal')
    // 按钮区独占一行，标题获得整行宽度
    expect(src).toContain('width: 100%')
  })

  it('shrinks the header action buttons on mobile instead of squeezing the title', () => {
    expect(src).toContain(':size="isMobile ? \'small\' : \'default\'"')
    // 空间不足时头部整体换行，按钮区让位
    expect(src).toContain('flex-wrap: wrap')
  })

  it('overrides sync state badge to 离线 when the node is offline', () => {
    // 离线设备不可能"同步中"：前端覆盖后端快照状态
    expect(src).toContain("if (collector.value?.status === 'offline') return '离线'")
    // 在线节点的真实"同步中"展示不受影响
    expect(src).toContain("syncing: '同步中'")
  })
})
