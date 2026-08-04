import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import DeviceBatchDeleteDialog from '@/components/device/DeviceBatchDeleteDialog.vue'
import type { EdgeDevice } from '@/api/edgeDevice'

// 方案 v3.3 §2.2 批量删除弹窗: 汇总视图 + 统一 radio (不逐台展示实例数)。

function makeDevice(id: number, logical = false): EdgeDevice {
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
    logical_device_id: logical ? 100 + id : undefined,
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

  it('汇总文案: 将删除 N 台设备，其中 M 台属于逻辑设备（数据默认保留）', async () => {
    const wrapper = mountDialog([
      makeDevice(1, true),
      makeDevice(2, true),
      makeDevice(3, false),
    ])
    await flushPromises()

    const summary = wrapper.find('.batch-summary')
    expect(summary.exists()).toBe(true)
    expect(summary.text()).toContain('3')
    expect(summary.text()).toContain('2')
    expect(summary.text()).toContain('逻辑设备')
    expect(summary.text()).toContain('数据默认保留')
  })

  it('无逻辑设备时汇总文案不声称逻辑设备台数', async () => {
    const wrapper = mountDialog([makeDevice(1), makeDevice(2)])
    await flushPromises()

    const text = wrapper.find('.batch-summary').text()
    expect(text).toContain('2')
    expect(text).toContain('均不属于逻辑设备')
  })

  it('统一一个 radio，默认选中全部保留历史数据', async () => {
    const wrapper = mountDialog([makeDevice(1, true)])
    await flushPromises()

    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    expect(radios).toHaveLength(2)
    expect((radios[0].element as HTMLInputElement).checked).toBe(true)
    expect((radios[1].element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.text()).toContain('全部保留历史数据')
  })

  it('默认确认 emit confirm(false)（全部保留）', async () => {
    const wrapper = mountDialog([makeDevice(1, true)])
    await wrapper.find('.el-dialog__footer .el-button--danger').trigger('click')
    expect(wrapper.emitted('confirm')).toHaveLength(1)
    expect(wrapper.emitted('confirm')![0]).toEqual([false])
  })

  it('选择全部删除后确认 emit confirm(true)，危险文案可见', async () => {
    const wrapper = mountDialog([makeDevice(1, true)])
    await flushPromises()

    expect(wrapper.text()).toContain('全部删除历史数据（将在后台删除，不可恢复）')

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
    const wrapper = mountDialog([makeDevice(1, true)])
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
