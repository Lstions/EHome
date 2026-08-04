import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import LogicalDeviceCandidateSelect from '@/components/device/LogicalDeviceCandidateSelect.vue'

// 方案 v3.3 §3.1/§1.3 — 创建继承候选列表组件。

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getCandidates: vi.fn(),
  },
}))

import { edgeDeviceApi } from '@/api/edgeDevice'

const mockGetCandidates = vi.mocked(edgeDeviceApi.getCandidates)

const candidate = (over: Partial<any> = {}) => ({
  id: 11,
  name: '旧BMS',
  device_type: 'bms',
  retention_days: 365,
  instance_count: 1,
  last_data_at: '2026-07-01T00:00:00Z',
  match_weight: 80,
  row_estimate: 5000,
  ...over,
})

const mountSelect = (props: Record<string, unknown> = {}) =>
  mount(LogicalDeviceCandidateSelect, {
    props: { type: 'bms', modelValue: null, ...props },
  })

describe('LogicalDeviceCandidateSelect.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockGetCandidates.mockResolvedValue([])
  })

  it('type 未就绪时显示引导且不请求 candidates', async () => {
    const wrapper = mountSelect({ type: '' })
    await flushPromises()
    expect(mockGetCandidates).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="candidate-awaiting-type"]').exists()).toBe(true)
  })

  // 回归: 不传 active 时必须正常加载 (Vue Boolean prop 缺省置 false 陷阱,
  // EdgeDeviceList 步骤 0 不传 active)。
  it('不传 active 时缺省视为激活并加载候选', async () => {
    mockGetCandidates.mockResolvedValue([candidate()])
    const wrapper = mount(LogicalDeviceCandidateSelect, {
      props: { type: 'bms', modelValue: null },
    })
    await flushPromises()
    expect(mockGetCandidates).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="candidate-list"]').exists()).toBe(true)
  })

  it('active=false (折叠区未展开) 时不发请求', async () => {
    mountSelect({ active: false })
    await flushPromises()
    expect(mockGetCandidates).not.toHaveBeenCalled()
  })

  it('加载候选列表并按权重渲染文案', async () => {
    mockGetCandidates.mockResolvedValue([
      candidate({ id: 11, name: '同节点BMS', match_weight: 80 }),
      candidate({ id: 12, name: '同类型BMS', match_weight: 20, row_estimate: undefined }),
    ])
    const wrapper = mountSelect({ nodeId: 'F0F5BDFFFE02', hardwareId: '0x76' })
    await flushPromises()

    expect(mockGetCandidates).toHaveBeenCalledWith({
      type: 'bms',
      node_id: 'F0F5BDFFFE02',
      hardware_id: '0x76',
      channel_id: undefined,
    })
    const list = wrapper.find('[data-testid="candidate-list"]')
    expect(list.exists()).toBe(true)
    expect(list.text()).toContain('同节点同地址')
    expect(list.text()).toContain('同类型')
    // row_estimate 缺省时不显示数据量段 (降级语义)
    expect(list.text()).not.toContain('约 undefined')
  })

  it('选中候选 emit id, 再次点击取消选择 emit null', async () => {
    mockGetCandidates.mockResolvedValue([candidate({ id: 11 })])
    const wrapper = mountSelect()
    await flushPromises()

    const radio = wrapper.find('input.candidate-radio')
    expect(radio.exists()).toBe(true)
    await radio.trigger('change')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([11])

    await wrapper.setProps({ modelValue: 11 })
    await radio.trigger('change')
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual([null])
  })

  it('加载失败显示降级提示且重试可恢复', async () => {
    mockGetCandidates.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mountSelect()
    await flushPromises()

    expect(wrapper.find('[data-testid="candidate-error"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('boom')

    mockGetCandidates.mockResolvedValue([candidate()])
    await wrapper.find('button').trigger('click') // 重试按钮
    await flushPromises()
    expect(wrapper.find('[data-testid="candidate-list"]').exists()).toBe(true)
  })

  it('空候选集显示"暂无可继承"引导', async () => {
    mockGetCandidates.mockResolvedValue([])
    const wrapper = mountSelect()
    await flushPromises()
    expect(wrapper.find('[data-testid="candidate-empty"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('该类型暂无可继承的历史数据')
  })

  it('type 变化后已选项不在新候选集时清空选择', async () => {
    mockGetCandidates.mockResolvedValue([candidate({ id: 11 })])
    const wrapper = mountSelect({ modelValue: 11 })
    await flushPromises()

    // type 变化 → 重查; 新候选集无 id=11 → emit null
    mockGetCandidates.mockResolvedValue([candidate({ id: 99 })])
    await wrapper.setProps({ type: 'sn3001' })
    await flushPromises()

    const emitted = wrapper.emitted('update:modelValue') || []
    expect(emitted.some(args => args[0] === null)).toBe(true)
  })

  it('已选项仍在新候选集时不重复清空', async () => {
    mockGetCandidates.mockResolvedValue([candidate({ id: 11 })])
    const wrapper = mountSelect({ modelValue: 11 })
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('加载中显示骨架屏 (loading skeleton)', async () => {
    let resolve!: (v: any[]) => void
    mockGetCandidates.mockReturnValue(new Promise(r => { resolve = r }))
    const wrapper = mountSelect()
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="candidate-loading"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('正在加载可继承的逻辑设备')
    resolve([])
    await flushPromises()
    expect(wrapper.find('[data-testid="candidate-loading"]').exists()).toBe(false)
  })

  it('active=false 时延迟加载, 激活后加载', async () => {
    const wrapper = mountSelect({ active: false })
    await flushPromises()
    expect(mockGetCandidates).not.toHaveBeenCalled()
    await wrapper.setProps({ active: true })
    await flushPromises()
    expect(mockGetCandidates).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="candidate-empty"]').exists()).toBe(true)
  })

  it('权重五档文案完整映射 (100/80/60/40/20)', async () => {
    mockGetCandidates.mockResolvedValue([
      candidate({ id: 1, match_weight: 100 }),
      candidate({ id: 2, match_weight: 80 }),
      candidate({ id: 3, match_weight: 60 }),
      candidate({ id: 4, match_weight: 40 }),
      candidate({ id: 5, match_weight: 20 }),
    ])
    const wrapper = mountSelect()
    await flushPromises()
    const text = wrapper.text()
    expect(text).toContain('原位置重建')
    expect(text).toContain('同节点同地址')
    expect(text).toContain('同节点')
    expect(text).toContain('同地址异节点')
    expect(text).toContain('同类型')
  })

  it('选中卡片带 selected 高亮 class', async () => {
    mockGetCandidates.mockResolvedValue([candidate({ id: 11 }), candidate({ id: 12 })])
    const wrapper = mountSelect({ modelValue: 12 })
    await flushPromises()
    const cards = wrapper.findAll('.candidate-card')
    expect(cards[0].classes()).not.toContain('selected')
    expect(cards[1].classes()).toContain('selected')
  })
})
