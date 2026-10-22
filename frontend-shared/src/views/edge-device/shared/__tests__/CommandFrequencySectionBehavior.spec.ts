import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const mocks = vi.hoisted(() => ({
  getCommandIntervals: vi.fn(),
  updateCommandIntervals: vi.fn(),
  getNodeDetail: vi.fn(),
}))

vi.mock('@/api/edgeDevice', () => ({ edgeDeviceApi: mocks }))
vi.mock('@/api/node', () => ({ nodeApi: { getDetail: mocks.getNodeDetail } }))
// feedback 工具内部调用 ElMessage(...) 函数形式，mock 必须可调用
vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
  ElMessageBox: {},
}))

import CommandFrequencySection from '../CommandFrequencySection.vue'

/**
 * 指令频率（共享组件）行为契约。
 *
 * 2026-10-22 第七轮把「BMS 私有轮询配置卡」与「逆变器/通用页 CommandList」合并成一个组件后，
 * 两套行为都必须还在。原 CommandListRedesign.spec 的用例按新组件重写（挂载点不变），
 * 并补上从 BMS 实现合并进来的容量门行为（超限点名 / 逐行禁用 / 保存判死）。
 */
const command = (over: Partial<{
  id: string; name: string; cmd_byte: number; interval_ms: number; current_interval_ms: number
  schedulable: boolean; description: string; write_data: string
}> = {}) => ({
  id: over.id ?? 'poll-combined',
  name: over.name ?? '读取综合信息',
  type: 'read',
  cmd_byte: over.cmd_byte ?? 0x0f,
  write_data: over.write_data ?? '',
  read_length: 7,
  delay_ms: 0,
  interval_ms: over.interval_ms ?? 5000,
  current_interval_ms: over.current_interval_ms ?? 5000,
  schedulable: over.schedulable ?? true,
  description: over.description ?? '综合信息轮询',
})

async function mountPanel(result = [command()], props: Record<string, unknown> = {}) {
  mocks.getCommandIntervals.mockResolvedValue(result)
  const wrapper = mount(CommandFrequencySection, {
    props: { deviceId: 1, ...props },
    attachTo: document.body,
  })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.updateCommandIntervals.mockResolvedValue(undefined)
  mocks.getNodeDetail.mockResolvedValue({ config_version: 'v1', config_sync_state: 'in_sync' })
})

describe('指令频率（共享组件）：状态语义与容器感知', () => {
  /*
   * 状态标签只表达状态（轮询中 / 已禁用），数值与单位归输入框那一路 ——
   * 否则同一行会出现两个 ms（「轮询中 · 100 ms  [100] ms」），读屏也会读成两个单位。
   */
  it('启用指令显示文字+颜色双通道状态（success 轮询中），禁用指令显示 info 已禁用', async () => {
    const wrapper = await mountPanel([
      command({ id: 'on', name: '读取综合信息', current_interval_ms: 100 }),
      command({ id: 'off', name: '读取基本信息', cmd_byte: 0x03, current_interval_ms: 0 }),
    ])

    const stateTags = wrapper.findAll('.cmd-state')
    expect(stateTags).toHaveLength(2)
    expect(stateTags[0].text()).toBe('轮询中')
    expect(stateTags[0].attributes('data-type')).toBe('success')
    expect(stateTags[1].text()).toBe('已禁用')
    expect(stateTags[1].attributes('data-type')).toBe('info')
    expect(stateTags[0].text()).not.toMatch(/\d/)
    expect(stateTags[0].text()).not.toContain('ms')
    wrapper.unmount()
  })

  it('开关 OFF 置 0 禁用；再 ON 恢复模板默认间隔（单一事实源 = interval 数值）', async () => {
    const wrapper = await mountPanel([
      command({ id: 'a', interval_ms: 3000, current_interval_ms: 3000 }),
    ])
    const sw = wrapper.find('.enable-switch')
    expect(sw.classes()).toContain('is-checked')

    await sw.trigger('click') // OFF
    expect(wrapper.find('.cmd-state').text()).toBe('已禁用')

    await sw.trigger('click') // ON → 恢复模板默认 3000
    expect(wrapper.find('.cmd-state').text()).toBe('轮询中')
    expect((wrapper.find('input.el-input-number').element as HTMLInputElement).value).toBe('3000')
    wrapper.unmount()
  })

  it('手动输入间隔：更新状态、解锁保存按钮；输入 0 即禁用', async () => {
    const wrapper = await mountPanel([command({ id: 'a', current_interval_ms: 5000 })])
    const input = wrapper.find('input.el-input-number')
    const saveBtn = wrapper.find('.command-actions .el-button')
    expect(saveBtn.attributes('disabled')).toBeDefined()

    await input.setValue('2000')
    expect(wrapper.find('.cmd-state').text()).toBe('轮询中')
    expect((input.element as HTMLInputElement).value).toBe('2000')
    expect(saveBtn.attributes('disabled')).toBeUndefined()

    await input.setValue('0')
    expect(wrapper.find('.cmd-state').text()).toBe('已禁用')
    wrapper.unmount()
  })

  it('清空输入框（undefined）归一为 0 禁用（fail-closed，状态 tag 立即可见）', async () => {
    const wrapper = await mountPanel([command({ id: 'a', current_interval_ms: 5000 })])
    const input = wrapper.find('input.el-input-number')
    await input.setValue('')
    expect(wrapper.find('.cmd-state').text()).toBe('已禁用')
    wrapper.unmount()
  })

  it('保存门禁：与节点一致时禁用并说明原因；有改动才允许保存，成功后基线刷新', async () => {
    const wrapper = await mountPanel([command({ id: 'a', current_interval_ms: 5000 })])
    const saveBtn = wrapper.find('.command-actions .el-button')

    expect(saveBtn.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('当前配置与节点一致')

    await wrapper.find('input.el-input-number').setValue('1000')
    expect(saveBtn.attributes('disabled')).toBeUndefined()

    await saveBtn.trigger('click')
    await flushPromises()

    expect(mocks.updateCommandIntervals).toHaveBeenCalledWith(1, { a: 1000 })
    expect(saveBtn.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('已保存')
    wrapper.unmount()
  })

  it('加载失败显性报错并提供重试；重试成功后渲染指令行', async () => {
    mocks.getCommandIntervals.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mount(CommandFrequencySection, { props: { deviceId: 1 }, attachTo: document.body })
    await flushPromises()

    expect(wrapper.find('.el-alert--error').exists()).toBe(true)
    expect(wrapper.text()).toContain('轮询指令加载失败')

    mocks.getCommandIntervals.mockResolvedValue([command()])
    await wrapper.find('.load-error-row .el-button').trigger('click')
    await flushPromises()

    expect(wrapper.find('.el-alert--error').exists()).toBe(false)
    expect(wrapper.text()).toContain('读取综合信息')
    wrapper.unmount()
  })
})

describe('指令频率（共享组件）：容量门（从 BMS 私有实现合并进来）', () => {
  it('超限：warning 告警点名「该禁用哪几条」+ 逐行「超限」标签 + 保存按钮判死', async () => {
    const wrapper = await mountPanel([
      command({ id: 'a', name: '读取运行状态', current_interval_ms: 10000 }),
      command({ id: 'b', name: '读取市电供电', current_interval_ms: 10000 }),
      command({ id: 'c', name: '读取输出信息', current_interval_ms: 10000 }),
    ], { maxPollingCommands: 2 })

    expect(wrapper.find('.el-alert--warning').exists()).toBe(true)
    expect(wrapper.text()).toContain('超过上限 2 条')
    // 点名：超出上限的那一条（第三条）必须是可读的指令名
    expect(wrapper.text()).toContain('读取输出信息')

    const stateTags = wrapper.findAll('.cmd-state')
    expect(stateTags.map(t => t.text())).toEqual(['轮询中', '轮询中', '超限'])
    expect(wrapper.find('.command-actions .el-button').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('超限行的「禁用」按钮一键置 0，恢复合法状态后可保存', async () => {
    const wrapper = await mountPanel([
      command({ id: 'a', name: 'A', current_interval_ms: 1000 }),
      command({ id: 'b', name: 'B', current_interval_ms: 1000 }),
    ], { maxPollingCommands: 1 })

    expect(wrapper.findAll('.excess-disable')).toHaveLength(1)
    await wrapper.find('.excess-disable').trigger('click')
    await flushPromises()

    expect(wrapper.findAll('.cmd-state').map(t => t.text())).toEqual(['轮询中', '已禁用'])
    expect(wrapper.find('.command-actions .el-button').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('占满：给出 info 提示（再启用任何一条都会超限）', async () => {
    const wrapper = await mountPanel([
      command({ id: 'a', current_interval_ms: 1000 }),
      command({ id: 'b', current_interval_ms: 1000 }),
    ], { maxPollingCommands: 2 })

    expect(wrapper.find('.el-alert--info').exists()).toBe(true)
    expect(wrapper.text()).toContain('已占满 2/2 条轮询额度')
    expect(wrapper.find('.command-actions .el-button').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('未上报上限：不显示容量告警（不假装有上限）', async () => {
    const wrapper = await mountPanel([command({ current_interval_ms: 1000 })])
    expect(wrapper.find('.el-alert--warning').exists()).toBe(false)
    expect(wrapper.find('.el-alert--info').exists()).toBe(false)
    wrapper.unmount()
  })

  it('启用条数经 usage-change 外抛，供页面页头摘要复用（不重复拉接口）', async () => {
    const wrapper = await mountPanel([
      command({ id: 'a', current_interval_ms: 1000 }),
      command({ id: 'b', current_interval_ms: 0 }),
    ], { maxPollingCommands: 4 })

    const events = wrapper.emitted('usage-change') ?? []
    expect(events.length).toBeGreaterThan(0)
    expect(events.at(-1)?.[0]).toMatchObject({ enabled: 1, limit: 4, overLimit: false })
    wrapper.unmount()
  })
})

describe('指令频率（共享组件）：reloadToken 由页面驱动刷新', () => {
  it('reloadToken 变化时重新拉取指令列表（页头「刷新数据」接线）', async () => {
    const wrapper = await mountPanel([command()])
    expect(mocks.getCommandIntervals).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ reloadToken: 1 })
    await flushPromises()
    expect(mocks.getCommandIntervals).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
