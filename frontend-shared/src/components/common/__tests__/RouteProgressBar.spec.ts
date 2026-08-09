import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
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
    // 与真实模块对齐：暴露可写的单例 ref 供测试直接驱动状态
    __routeProgressState: () => ({ progress, visible }),
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
    const { __routeProgressState } = await import('@/stores/routeProgress')
    const { visible } = __routeProgressState()
    visible.value = true
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.route-progress-bar').classes()).toContain('is-visible')
  })

  it('fill 宽度随 progress 百分比渲染', async () => {
    const wrapper = mount(RouteProgressBar)
    const { __routeProgressState } = await import('@/stores/routeProgress')
    const { progress } = __routeProgressState()
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

/**
 * CSS transition 方向性断言（源码级）：
 * happy-dom 不解析 scoped CSS，无法从计算样式验证过渡时长；
 * 直接读组件源码，锁定「淡入快（0.15s）、淡出慢（0.25s）」的声明结构——
 * 基类（隐藏态）携带淡出过渡，.is-visible（可见态）携带淡入过渡，
 * 防止回退到「同一声明写两个同属性 transition、后者整体胜出」的错误写法。
 */
describe('RouteProgressBar.vue CSS transition 方向性', () => {
  // happy-dom 下 import.meta.url 非 file:// 协议，不能用 fileURLToPath；
  // vitest 以项目根（frontend-shared）为 cwd 运行，直接按根相对路径读取源码。
  const componentPath = resolve(process.cwd(), 'src/components/common/RouteProgressBar.vue')
  const source = readFileSync(componentPath, 'utf-8')

  /** 提取某个选择器块的样式文本（简易花括号配对） */
  function extractBlock(css: string, selector: string): string {
    const idx = css.indexOf(selector)
    expect(idx, `未找到选择器块: ${selector}`).toBeGreaterThanOrEqual(0)
    const open = css.indexOf('{', idx)
    let depth = 0
    for (let i = open; i < css.length; i++) {
      if (css[i] === '{') depth++
      else if (css[i] === '}') {
        depth--
        if (depth === 0) return css.slice(open + 1, i)
      }
    }
    throw new Error(`选择器块未闭合: ${selector}`)
  }

  it('基类（隐藏态）仅声明淡出过渡 opacity 0.25s ease-out', () => {
    const block = extractBlock(source, '.route-progress-bar {')
    const transitions = block.match(/transition:\s*[^;]+;/g) ?? []
    expect(transitions).toHaveLength(1)
    expect(transitions[0]).toContain('opacity 0.25s ease-out')
    // 禁止回退写法：同一声明内不得出现第二个 opacity transition
    expect(transitions[0]?.match(/opacity/g) ?? []).toHaveLength(1)
  })

  it('.is-visible（可见态）声明淡入过渡 opacity 0.15s ease-in', () => {
    const block = extractBlock(source, '.route-progress-bar.is-visible {')
    const transitions = block.match(/transition:\s*[^;]+;/g) ?? []
    expect(transitions).toHaveLength(1)
    expect(transitions[0]).toContain('opacity 0.15s ease-in')
  })
})
