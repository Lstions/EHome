import { describe, expect, it, beforeEach, vi } from 'vitest'
import { ref, nextTick, h } from 'vue'
import { mount } from '@vue/test-utils'
import { useCollapseState } from '@/composables/useCollapseState'
import CollapseBlock from '@/views/edge-device/bms/CollapseBlock.vue'
import bmsDetailSource from '@/views/edge-device/bms/BmsDetailPage.vue?raw'

/**
 * BMS 移动端改版第 5 步：诊断类区块折叠机制（2026-10-20）。
 *
 * 这三条是这一层最容易悄悄坏掉、而且坏了页面「看起来还正常」的东西：
 *  1) 折叠记忆按设备隔离 + 只存展开项 → 坏了表现为「换设备后展开状态串台」或「刷新后全丢」
 *  2) 桌面零包装 → 坏了表现为桌面少几个 flex 项（`.row > .card` 选择器失配），整页布局塌
 *  3) 默认折叠 + 摘要常显 → 坏了表现为诊断区又变回 8 屏长
 */

const STORAGE_KEY = (id: number | string) => 'bms-collapse:' + id

describe('useCollapseState：按设备隔离的折叠记忆', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('默认全部收起（不给默认展开项）；toggle 后只把已展开项写进 localStorage', async () => {
    const deviceId = ref<number | string>(14)
    const collapse = useCollapseState(() => deviceId.value)
    expect(collapse.isOpen('charts')).toBe(false)
    collapse.toggle('charts')
    expect(collapse.isOpen('charts')).toBe(true)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY(14)) || '[]')).toEqual(['charts'])
    collapse.toggle('charts')
    expect(collapse.isOpen('charts')).toBe(false)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY(14)) || '[]')).toEqual([])
  })

  it('换设备后读取该设备自己的记忆（不串台）', async () => {
    const deviceId = ref<number | string>(14)
    const collapse = useCollapseState(() => deviceId.value)
    collapse.toggle('charts')
    deviceId.value = 15
    await nextTick()
    expect(collapse.isOpen('charts')).toBe(false)
    collapse.toggle('stream')
    deviceId.value = 14
    await nextTick()
    // 14 号设备仍是「历史趋势展开」，15 号设备的 stream 不影响它
    expect(collapse.isOpen('charts')).toBe(true)
    expect(collapse.isOpen('stream')).toBe(false)
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY(15)) || '[]')).toEqual(['stream'])
  })

  it('localStorage 不可用（隐私模式/配额满）时退化为内存态，不抛错也不锁死折叠', async () => {
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('denied') })
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota') })
    try {
      const collapse = useCollapseState(() => 14)
      expect(collapse.isOpen('charts')).toBe(false)
      collapse.toggle('charts')
      expect(collapse.isOpen('charts')).toBe(true)
      collapse.toggle('charts')
      expect(collapse.isOpen('charts')).toBe(false)
    } finally {
      getItem.mockRestore()
      setItem.mockRestore()
    }
  })

  it('deviceId 缺失/为 0 时不写脏 key', async () => {
    const collapse = useCollapseState(() => 0)
    collapse.toggle('charts')
    expect(collapse.isOpen('charts')).toBe(true)
    expect(localStorage.getItem('bms-collapse:0')).toBeNull()
  })
})

describe('CollapseBlock：桌面零包装、窄屏折叠头', () => {
  const slot = () => h('div', { class: 'inner-card' }, '内容')

  it('桌面（collapsible=false）不产生任何包装节点 —— 否则父页 `.row > .card` 直接子选择器全部失配', () => {
    const wrapper = mount(CollapseBlock, {
      props: { blockId: 'history', title: '操作历史', collapsible: false, open: false },
      slots: { default: slot },
    })
    // 插槽内容直接成为根片段：DOM 上不多任何一层包装节点
    expect(wrapper.find('.collapse-block').exists()).toBe(false)
    expect(wrapper.find('.collapse-head').exists()).toBe(false)
    expect(wrapper.find('.collapse-body').exists()).toBe(false)
    expect(wrapper.find('.inner-card').exists()).toBe(true)
    expect(wrapper.html()).toBe('<div class="inner-card">内容</div>')
  })

  it('窄屏：标题 + 一行摘要 + aria 状态，收起时正文 display:none 但组件保活', async () => {
    const wrapper = mount(CollapseBlock, {
      props: { blockId: 'charts', title: '历史趋势', summary: '压差 12mV · 总电压 51.76V', collapsible: true, open: false },
      slots: { default: slot },
    })
    const head = wrapper.find('.collapse-head')
    expect(head.exists()).toBe(true)
    expect(head.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('.collapse-title').text()).toBe('历史趋势')
    expect(wrapper.find('.collapse-summary').text()).toContain('压差 12mV')
    // 正文不是 v-if 移除：图表组件要保活（展开时不重新取数/不重新 init）
    expect(wrapper.find('.inner-card').exists()).toBe(true)
    expect((wrapper.find('.collapse-body').element as HTMLElement).style.display).toBe('none')

    await head.trigger('click')
    expect(wrapper.emitted('toggle')).toHaveLength(1)

    await wrapper.setProps({ open: true })
    expect(head.attributes('aria-expanded')).toBe('true')
    expect((wrapper.find('.collapse-body').element as HTMLElement).style.display).toBe('')
  })
})

describe('BmsDetailPage 接入契约（源码层）', () => {
  it('折叠状态按路由 deviceId 记忆（不是写死或全局单例）', () => {
    expect(bmsDetailSource).toContain('useCollapseState(() => deviceId.value)')
  })

  it('诊断类区块走 CollapseBlock 且默认折叠（不传 open 默认值 → collapse.isOpen 初始 false）', () => {
    for (const blockId of ['charts', 'stream', 'history']) {
      expect(bmsDetailSource).toContain(`block-id="${blockId}"`)
    }
    // 关键反回退：不能写成 :open="true" 之类的强制展开
    expect(bmsDetailSource).not.toMatch(/block-id="charts"[\s\S]{0,200}?open="true"/)
  })

  it('折叠态摘要来自真实数据（压差/总电压、实时条数、历史条数），不是静态文案', () => {
    expect(bmsDetailSource).toContain('const chartsCollapseSummary = computed(')
    expect(bmsDetailSource).toContain('const streamCollapseSummary = computed(')
    expect(bmsDetailSource).toContain('const historyCollapseSummary = computed(')
  })

  it('窄屏隐藏卡片内重复标题（折叠头已经写了区块名），桌面媒体查询外不生效', () => {
    expect(bmsDetailSource).toMatch(/\.stream-card > \.card-head > \.card-title,[\s\S]{0,120}?\.history-card > \.card-head > \.card-title \{ display: none; \}/)
  })
})