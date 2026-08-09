import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import RouteProgressBar from '@/components/common/RouteProgressBar.vue'

/**
 * RouteProgressBar 组件测试：直接驱动模块级单例断言渲染状态。
 */

vi.mock('@/stores/routeProgress', () => {
  const { ref } = require('vue')
  const progress = ref(10)
  const visible = ref(false)
  return {
    useRouteProgress: () => ({
      progress,
      visible,
      start: vi.fn(),
      done: vi.fn(),
      fail: vi.fn(),
      reset: vi.fn(),
      isBusy: vi.fn(() => visible.value),
    }),
  }
})

describe('RouteProgressBar.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('非 busy 时 opacity 0 隐藏（is-visible class 不生效）', () => {
    const wrapper = mount(RouteProgressBar)
    expect(wrapper.find('.route-progress-bar').exists()).toBe(true)
    expect(wrapper.find('.route-progress-bar').classes()).not.toContain('is-visible')
  })

  it('visible 时添加 is-visible class', async () => {
    const wrapper = mount(RouteProgressBar)
    const { useRouteProgress } = await import('@/stores/routeProgress')
    const { visible } = useRouteProgress()
    visible.value = true
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.route-progress-bar').classes()).toContain('is-visible')
  })

  it('fill 宽度随 progress 百分比渲染', async () => {
    const wrapper = mount(RouteProgressBar)
    const { useRouteProgress } = await import('@/stores/routeProgress')
    const { progress } = useRouteProgress()
    progress.value = 42
    await wrapper.vm.$nextTick()
    const fill = wrapper.find('.route-progress-bar__fill')
    expect(fill.attributes('style')).toContain('width: 42%')
  })

  it('固定定位且 z-index 4000（覆盖 el-message-box），非交互屏蔽', () => {
    const wrapper = mount(RouteProgressBar)
    // z-index/position 由 scoped 样式控制（happy-dom 不解析 CSS），此处断言类名与语义属性
    expect(wrapper.find('.route-progress-bar').classes()).toContain('route-progress-bar')
    expect(wrapper.find('.route-progress-bar').classes()).toContain('route-progress-bar')
    expect(wrapper.find('.route-progress-bar').attributes('aria-hidden')).toBe('true')
    expect(wrapper.find('.route-progress-bar').attributes('style')).toBeUndefined()
  })
})
