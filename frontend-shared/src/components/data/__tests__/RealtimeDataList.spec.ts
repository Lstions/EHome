import { nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
import type { DataItem } from '@/types/realtime'

// 全局 Element Plus stub 已在 src/test-setup.ts 注册：
// ElButton/ElTag/ElIcon 由全局 stub 提供；EmptyState 为真实组件；
// ElRadioGroup/ElRadioButton 为交互式 stub（RADIO_GROUP_KEY 注入协议），
// displayMode 切换走真实点击路径。

const items: DataItem[] = [
  {
    id: 'reading-1',
    timestamp: '2026-07-13T06:00:00Z',
    data: { voltage: 12.5 },
    rawData: [0x01, 0xaf],
    isRealtime: true,
  },
  {
    id: 'reading-2',
    timestamp: '2026-07-13T05:59:00Z',
    data: { voltage: 12.4 },
    isRealtime: false,
  },
]

function mountList(
  props: { items?: DataItem[]; autoScroll?: boolean; onClear?: () => void } = {},
): VueWrapper {
  return mount(RealtimeDataList, {
    props: {
      items: props.items ?? items,
      autoScroll: props.autoScroll ?? true,
      onClear: props.onClear,
    } as any,
  })
}

describe('RealtimeDataList 普通滚动列表', () => {
  it('按内容自然行高渲染明文数据与实时/历史标记', () => {
    const wrapper = mountList()

    expect(wrapper.find('.plain-list').exists()).toBe(true)
    expect(wrapper.findAll('.data-item')).toHaveLength(2)
    expect(wrapper.text()).toContain('voltage: 12.50')
    expect(wrapper.text()).toContain('实时')
    expect(wrapper.text()).toContain('历史')
  })

  it('真实点击16进制按钮显示原始帧，并通过父级 listener 清空', async () => {
    const onClear = vi.fn()
    const wrapper = mountList({ onClear })

    // 真实点击「16进制」radio button（走 RADIO_GROUP_KEY 注入协议）
    const hexButton = wrapper.findAll('.el-radio-button').find(b => b.text() === '16进制')
    expect(hexButton).toBeDefined()
    await hexButton!.trigger('click')
    await nextTick()

    expect(wrapper.find('.item-content').text()).toBe('01 AF')
    await wrapper.find('.list-stats .el-button').trigger('click')
    expect(onClear).toHaveBeenCalledOnce()
  })

  it('新数据到达时滚回顶部（最新在前）', async () => {
    const wrapper = mountList()
    const list = wrapper.find('.plain-list').element as HTMLElement
    list.scrollTop = 160

    await wrapper.setProps({
      items: [
        {
          id: 'reading-3',
          timestamp: '2026-07-13T06:01:00Z',
          data: { voltage: 12.6 },
          isRealtime: true,
        },
        ...items,
      ],
    })
    await nextTick()

    expect(list.scrollTop).toBe(0)
  })

  it('满额截断（length 恒定、首条 id 变化）时仍触发滚顶', async () => {
    // 模拟上游 maxItems 截断：进一条出一条，length 不变但首条 id 变化
    const wrapper = mountList({ items: [items[0], items[1]] })
    const list = wrapper.find('.plain-list').element as HTMLElement
    list.scrollTop = 160

    await wrapper.setProps({
      items: [
        { id: 'reading-new', timestamp: '2026-07-13T06:02:00Z', data: { voltage: 12.7 }, isRealtime: true },
        items[0],
      ],
    })
    await nextTick()

    expect(list.scrollTop).toBe(0)
  })

  it('空数据渲染共享 EmptyState 初始态，清空按钮禁用', () => {
    const wrapper = mountList({ items: [] })

    expect(wrapper.find('.plain-list').exists()).toBe(false)
    expect(wrapper.find('.empty-state').exists()).toBe(true)
    expect(wrapper.text()).toContain('暂无实时数据')
    expect(wrapper.text()).toContain('等待设备上报')
    expect(wrapper.find('.list-stats .el-button').attributes('disabled')).toBeDefined()
  })

  it('明文模式格式化结果为空时显示中性占位', () => {
    const wrapper = mountList({
      items: [{ id: 'empty-frame', timestamp: '2026-07-13T06:00:00Z', data: {}, isRealtime: true }],
    })

    expect(wrapper.find('.item-content').text()).toBe('(无数据字段)')
  })

  it('16进制模式空对象帧不伪装成真实帧（无 7B 7D 误导），显示中性占位', async () => {
    const wrapper = mountList({
      items: [{ id: 'empty-hex', timestamp: '2026-07-13T06:00:00Z', data: {}, isRealtime: true }],
    })

    const hexButton = wrapper.findAll('.el-radio-button').find(b => b.text() === '16进制')
    await hexButton!.trigger('click')
    await nextTick()

    expect(wrapper.find('.item-content').text()).toBe('(无数据字段)')
    expect(wrapper.text()).not.toContain('7B 7D')
  })
})
