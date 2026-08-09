import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const mocks = vi.hoisted(() => ({
  getCommandIntervals: vi.fn(),
  updateCommandIntervals: vi.fn(),
}))

vi.mock('@/api/edgeDevice', () => ({ edgeDeviceApi: mocks }))
// feedback 工具内部调用 ElMessage(...) 函数形式，mock 必须可调用
vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
  ElMessageBox: {},
}))

import CommandList from '../CommandList.vue'

/** BMS 指令模板（与实机截图同款 5 条） */
const command = (over: Partial<{ id: string; name: string; cmd_byte: number; interval_ms: number; current_interval_ms: number; schedulable: boolean; description: string }> = {}) => ({
  id: over.id ?? 'poll-combined',
  name: over.name ?? '读取综合信息',
  type: 'read',
  cmd_byte: over.cmd_byte ?? 0x0f,
  write_data: '',
  read_length: 7,
  delay_ms: 0,
  interval_ms: over.interval_ms ?? 5000,
  current_interval_ms: over.current_interval_ms ?? 5000,
  schedulable: over.schedulable ?? true,
  description: over.description ?? '综合信息轮询',
})

async function mountList(result = [command()]) {
  mocks.getCommandIntervals.mockResolvedValue(result)
  const wrapper = mount(CommandList, {
    props: { deviceId: 1 },
    attachTo: document.body,
  })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.updateCommandIntervals.mockResolvedValue(undefined)
})

describe('CommandList 重设计：状态语义与容器感知', () => {
  it('启用指令显示文字+颜色双通道状态（success 轮询中），禁用指令显示 info 已禁用', async () => {
    const wrapper = await mountList([
      command({ id: 'on', name: '读取综合信息', current_interval_ms: 100 }),
      command({ id: 'off', name: '读取基本信息', cmd_byte: 0x03, current_interval_ms: 0 }),
    ])

    const stateTags = wrapper.findAll('.cmd-state')
    expect(stateTags).toHaveLength(2)
    expect(stateTags[0].text()).toBe('轮询中 · 100 ms')
    expect(stateTags[0].attributes('data-type')).toBe('success')
    expect(stateTags[1].text()).toBe('已禁用')
    expect(stateTags[1].attributes('data-type')).toBe('info')
    wrapper.unmount()
  })

  it('开关 OFF 置 0 禁用；再 ON 恢复模板默认间隔（单一事实源 = interval 数值）', async () => {
    const wrapper = await mountList([
      command({ id: 'a', interval_ms: 3000, current_interval_ms: 3000 }),
    ])
    const sw = wrapper.find('.enable-switch')
    expect(sw.classes()).toContain('is-checked')

    await sw.trigger('click') // OFF
    expect(wrapper.find('.cmd-state').text()).toBe('已禁用')

    await sw.trigger('click') // ON → 恢复模板默认 3000
    expect(wrapper.find('.cmd-state').text()).toBe('轮询中 · 3000 ms')
    wrapper.unmount()
  })

  it('手动输入间隔：更新状态、解锁保存按钮；输入 0 即禁用', async () => {
    const wrapper = await mountList([command({ id: 'a', current_interval_ms: 5000 })])
    const input = wrapper.find('input.el-input-number')
    const saveBtn = wrapper.find('.command-actions .el-button')
    expect(saveBtn.attributes('disabled')).toBeDefined()

    await input.setValue('2000')
    expect(wrapper.find('.cmd-state').text()).toBe('轮询中 · 2000 ms')
    expect(saveBtn.attributes('disabled')).toBeUndefined()

    await input.setValue('0')
    expect(wrapper.find('.cmd-state').text()).toBe('已禁用')
    wrapper.unmount()
  })

  it('清空输入框（undefined）归一为 0 禁用（fail-closed，状态 tag 立即可见）', async () => {
    const wrapper = await mountList([command({ id: 'a', current_interval_ms: 5000 })])
    const input = wrapper.find('input.el-input-number')

    await input.setValue('')
    expect(wrapper.find('.cmd-state').text()).toBe('已禁用')
    wrapper.unmount()
  })

  it('保存门禁：与节点一致时禁用并说明原因；有改动才允许保存，成功后基线刷新', async () => {
    const wrapper = await mountList([command({ id: 'a', current_interval_ms: 5000 })])
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
    const wrapper = mount(CommandList, {
      props: { deviceId: 1 },
      attachTo: document.body,
    })
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
