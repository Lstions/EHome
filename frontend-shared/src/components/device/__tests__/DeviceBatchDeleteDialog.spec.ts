import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import DeviceBatchDeleteDialog from '@/components/device/DeviceBatchDeleteDialog.vue'
import type { EdgeDevice } from '@/api/edgeDevice'

// 方案 v3.3 §2.2 批量删除弹窗: 汇总视图 + 统一 radio (不逐台展示数据量)。
//
// 2026-10-23 逻辑设备移除（契约 §3）：原「其中 N 台属于逻辑设备（数据默认保留）」
// 统计口径已不存在 —— 逻辑设备概念层整体下线，数据处置策略不再因设备而异。
// 本文件相应删掉「logicalCount」两条用例，改为锁住新的中性汇总文案。

function makeDevice(id: number): EdgeDevice {
  return {
    id,
    node_id: 'F0F5BDFFFE02',
    channel_id: 1,
    name: `Device-${id}`,
    device_type: 'bmp280',
    protocol: 'modbus',
    hardware_type: 'i2c',
    hardware_id: `0x${id.toString(16).padStart(2, '0')}`,
    config: {},
    status: 'active',
    last_data: null,
    last_data_time: null,
    created_at: '2026-08-01T00:00:00Z',
  }
}

function mountDialog(devices: EdgeDevice[]) {
  return mount(DeviceBatchDeleteDialog, {
    props: { visible: true, devices, submitting: false },
  })
}

describe('DeviceBatchDeleteDialog.vue (方案 v3.3 §2.2)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('汇总文案: 只报台数，不再统计「属于逻辑设备」的台数', async () => {
    const wrapper = mountDialog([makeDevice(1), makeDevice(2), makeDevice(3)])
    await flushPromises()

    const summary = wrapper.find('.batch-summary')
    expect(summary.exists()).toBe(true)
    expect(summary.text()).toContain('3')
    expect(summary.text()).toContain('台设备')
    // 逻辑设备概念已整体移除：文案里不得再出现该词（用户无需理解内部模型）
    expect(summary.text()).not.toContain('逻辑设备')
  })

  it('提示语引导用户到单删流程查看数据量（不承诺批量展示）', async () => {
    const wrapper = mountDialog([makeDevice(1)])
    await flushPromises()
    const hint = wrapper.find('.batch-hint')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toContain('数据量估算')
  })

  it('统一一个 radio，默认选中全部保留历史数据', async () => {
    const wrapper = mountDialog([makeDevice(1)])
    await flushPromises()

    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    expect(radios).toHaveLength(2)
    expect((radios[0].element as HTMLInputElement).checked).toBe(true)
    expect((radios[1].element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.text()).toContain('全部保留历史数据')
  })

  it('默认确认 emit confirm(false)（全部保留）', async () => {
    const wrapper = mountDialog([makeDevice(1)])
    await wrapper.find('.el-dialog__footer .el-button--danger').trigger('click')
    expect(wrapper.emitted('confirm')).toHaveLength(1)
    expect(wrapper.emitted('confirm')![0]).toEqual([false])
  })

  it('选择全部删除后确认 emit confirm(true)，危险文案可见', async () => {
    const wrapper = mountDialog([makeDevice(1)])
    await flushPromises()

    // 2026-10-25 文案修正：删除设备行是软删（可恢复），只有清理开始后删数据才不可恢复
    expect(wrapper.text()).toContain('全部删除历史数据（后台分批清理；清理开始后不可恢复）')
    expect(wrapper.text()).toContain('恢复已删除设备')

    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    await radios[1].setValue(true)
    await flushPromises()
    expect((radios[1].element as HTMLInputElement).checked).toBe(true)

    await wrapper.find('.el-dialog__footer .el-button--danger').trigger('click')
    expect(wrapper.emitted('confirm')![0]).toEqual([true])
  })

  it('取消按钮关闭弹窗', async () => {
    const wrapper = mountDialog([makeDevice(1)])
    await wrapper.find('.el-dialog__footer .el-button--default').trigger('click')
    expect(wrapper.emitted('update:visible')![0]).toEqual([false])
  })

  it('重新打开时 radio 重置为全部保留', async () => {
    const wrapper = mountDialog([makeDevice(1)])
    await flushPromises()
    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    await radios[1].setValue(true)

    await wrapper.setProps({ visible: false })
    await wrapper.setProps({ visible: true })
    await flushPromises()

    const fresh = wrapper.findAll('.data-action-group input[type="radio"]')
    expect((fresh[0].element as HTMLInputElement).checked).toBe(true)
    expect((fresh[1].element as HTMLInputElement).checked).toBe(false)
  })
})
