import { describe, expect, it } from 'vitest'
import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'
import CollapseBlock from '@/views/edge-device/bms/CollapseBlock.vue'

/**
 * CollapseBlock 懒挂载契约（BMS 慢修复契约 §2.4 单测）。
 *
 * 背景（根因 R3）：窄屏 7 个诊断区块默认收起，但 CollapseBlock 用 display:none 保活子组件
 * ⇒ 用户看不到的 protection 30d / 温度 / SOC 三张卡在首屏就发重查询。
 * 新语义：collapsible && lazy 时「首次展开前不渲染正文」，首次展开后**保活**（不再卸载）。
 * 非 lazy / 桌面 collapsible=false 必须与改前完全一致（既有结构契约测试已锁）。
 */

let mountCount = 0
/** 用一个计数组件证明「没渲染」与「挂载后不卸载」—— DOM 存在性本身测不出重新挂载 */
const Heavy = defineComponent({
  name: 'Heavy',
  setup() {
    mountCount += 1
    return () => h('div', { class: 'heavy' }, '重内容')
  },
})

const heavySlot = () => h(Heavy)

function mountBlock(props: Record<string, unknown>) {
  return mount(CollapseBlock, {
    props: { blockId: 'charts', title: '历史趋势', ...props },
    slots: { default: heavySlot },
  })
}

describe('CollapseBlock lazy：首次展开前不渲染正文', () => {
  it('lazy + collapsible + 收起：折叠头照常渲染，正文组件根本不创建', () => {
    mountCount = 0
    const wrapper = mountBlock({ collapsible: true, open: false, lazy: true })
    expect(wrapper.find('.collapse-head').exists()).toBe(true)
    expect(wrapper.find('.collapse-head').attributes('aria-expanded')).toBe('false')
    // body 占位仍在（aria-controls 指向它），但里面没有子组件
    expect(wrapper.find('.collapse-body').exists()).toBe(true)
    expect(wrapper.find('.heavy').exists()).toBe(false)
    expect(mountCount).toBe(0)
  })

  it('首次 open 后渲染正文，再次收起不卸载（保活），重复展开也不重新挂载', async () => {
    mountCount = 0
    const wrapper = mountBlock({ collapsible: true, open: false, lazy: true })
    await wrapper.setProps({ open: true })
    expect(wrapper.find('.heavy').exists()).toBe(true)
    expect(mountCount).toBe(1)

    await wrapper.setProps({ open: false })
    // 收起后仍在 DOM 里（display:none），组件实例没被销毁
    expect(wrapper.find('.heavy').exists()).toBe(true)
    expect((wrapper.find('.collapse-body').element as HTMLElement).style.display).toBe('none')
    expect(mountCount).toBe(1)

    await wrapper.setProps({ open: true })
    expect((wrapper.find('.collapse-body').element as HTMLElement).style.display).toBe('')
    expect(mountCount).toBe(1) // 从未重新挂载
  })

  it('初始 open=true（按设备记忆恢复为展开）时首帧就渲染正文', () => {
    mountCount = 0
    const wrapper = mountBlock({ collapsible: true, open: true, lazy: true })
    expect(wrapper.find('.heavy').exists()).toBe(true)
    expect(mountCount).toBe(1)
  })
})

describe('CollapseBlock 非 lazy：行为与改前一致', () => {
  it('默认 lazy=false：收起时正文仍挂载并 display:none（原有保活语义）', () => {
    mountCount = 0
    const wrapper = mountBlock({ collapsible: true, open: false })
    expect(wrapper.find('.heavy').exists()).toBe(true)
    expect((wrapper.find('.collapse-body').element as HTMLElement).style.display).toBe('none')
    expect(mountCount).toBe(1)
  })

  it('桌面 collapsible=false + lazy=true：仍然零包装直出插槽（lazy 不影响桌面）', () => {
    mountCount = 0
    const wrapper = mountBlock({ collapsible: false, open: false, lazy: true })
    expect(wrapper.find('.collapse-block').exists()).toBe(false)
    expect(wrapper.find('.collapse-head').exists()).toBe(false)
    expect(wrapper.find('.heavy').exists()).toBe(true)
    expect(wrapper.html()).toBe('<div class="heavy">重内容</div>')
  })
})
