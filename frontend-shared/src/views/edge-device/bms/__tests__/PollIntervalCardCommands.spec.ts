import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { CommandTemplateWithInterval } from '@/api/edgeDevice'
import PollIntervalCard from '@/views/edge-device/bms/diag/PollIntervalCard.vue'

/**
 * PollIntervalCard「父页下发 commands」契约（BMS 慢修复契约 §2.6 单测）。
 *
 * 根因 R7：页面与 PollIntervalCard 各自调 GET /edge-devices/:id/commands（同毫秒发出，commands ×2）。
 * 新契约：父页是唯一取数入口，把原始返回通过 commands prop 下发；本卡只在**没人负责取数**时自取。
 * 这里锁住三种入口：
 *  ① commands 非 null ⇒ 一次请求都不发（只过滤 schedulable）；
 *  ② 父页取数中（parentLoading=true 且 commands=null）⇒ 等待，不发请求；
 *  ③ 旧用法 / 父页失败（commands=null 且 parentLoading=false）⇒ 回退自取数（失败态 + 重试可用）。
 */

const { getCommandIntervals } = vi.hoisted(() => ({ getCommandIntervals: vi.fn() }))

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getCommandIntervals: (...args: unknown[]) => getCommandIntervals(...args),
  },
}))

function cmd(
  id: string,
  overrides: Partial<CommandTemplateWithInterval> = {},
): CommandTemplateWithInterval {
  return {
    id,
    name: id,
    type: 'read',
    cmd_byte: 0,
    write_data: '',
    read_length: 0,
    delay_ms: 0,
    interval_ms: 1000,
    schedulable: true,
    description: '',
    current_interval_ms: 1000,
    ...overrides,
  }
}

beforeEach(() => {
  getCommandIntervals.mockReset()
})

describe('父页下发 commands：唯一取数入口，本卡不再请求', () => {
  it('commands 非 null：不发请求、只按 schedulable 过滤、摘要正确', async () => {
    const wrapper = mount(PollIntervalCard, {
      props: {
        deviceId: 14,
        online: true,
        commands: [cmd('read_comprehensive'), cmd('b', { schedulable: false })],
        parentLoading: false,
      },
    })
    await flushPromises()
    expect(getCommandIntervals).not.toHaveBeenCalled()
    expect(wrapper.findAll('.bar-row')).toHaveLength(1) // 不可调度项被过滤
    expect(wrapper.emitted('summary')?.at(-1)?.[0]).toBe('1 条轮询中')
  })

  it('父页取数中（commands=null + parentLoading）：等待、不发请求；数据到达后渲染且仍不请求', async () => {
    const wrapper = mount(PollIntervalCard, {
      props: { deviceId: 14, online: true, commands: null, parentLoading: true },
    })
    await flushPromises()
    expect(getCommandIntervals).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('正在加载轮询配置')

    await wrapper.setProps({ commands: [cmd('read_comprehensive')], parentLoading: false })
    await flushPromises()
    expect(getCommandIntervals).not.toHaveBeenCalled()
    expect(wrapper.findAll('.bar-row')).toHaveLength(1)
  })

  it('commands 变化（父页重新取数后下发）：watch 重建列表，不请求', async () => {
    const wrapper = mount(PollIntervalCard, {
      props: { deviceId: 14, online: true, commands: [cmd('a')], parentLoading: false },
    })
    await flushPromises()
    expect(wrapper.findAll('.bar-row')).toHaveLength(1)
    await wrapper.setProps({ commands: [cmd('a'), cmd('b', { current_interval_ms: 5000 })] })
    await flushPromises()
    expect(wrapper.findAll('.bar-row')).toHaveLength(2)
    expect(getCommandIntervals).not.toHaveBeenCalled()
  })
})

describe('旧行为 / 父页失败：回退到自取数', () => {
  it('不传 commands（默认 null、parentLoading 默认 false）：保持旧行为自取数', async () => {
    getCommandIntervals.mockResolvedValue([cmd('a'), cmd('b', { schedulable: false })])
    const wrapper = mount(PollIntervalCard, { props: { deviceId: 14, online: true } })
    await flushPromises()
    expect(getCommandIntervals).toHaveBeenCalledTimes(1)
    expect(getCommandIntervals).toHaveBeenCalledWith(14)
    expect(wrapper.findAll('.bar-row')).toHaveLength(1)
  })

  it('父页取数失败（loading 结束仍为 null）：本卡回退取数并渲染「加载失败 + 重试」', async () => {
    const wrapper = mount(PollIntervalCard, {
      props: { deviceId: 14, online: true, commands: null, parentLoading: true },
    })
    await flushPromises()
    expect(getCommandIntervals).not.toHaveBeenCalled()

    getCommandIntervals.mockRejectedValueOnce(new Error('请求超时'))
    await wrapper.setProps({ parentLoading: false })
    await flushPromises()
    expect(getCommandIntervals).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('轮询配置加载失败')
    expect(wrapper.text()).toContain('请求超时')
    const retry = wrapper.find('.state-line.is-error button')
    expect(retry.exists()).toBe(true)

    getCommandIntervals.mockResolvedValueOnce([cmd('a')])
    await retry.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.bar-row')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('轮询配置加载失败')
  })
})
