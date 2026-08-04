import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import DeviceDeleteDialog from '@/components/device/DeviceDeleteDialog.vue'
import type { EdgeDevice } from '@/api/edgeDevice'

// 方案 v3.3 §2.1 单删弹窗: 基本信息即时显示 / 逻辑设备信息异步加载降级 /
// radio 默认保留 / delete_data 两条提交路径。

const mockGetLogicalDeviceInfo = vi.fn()

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getLogicalDeviceInfo: (...args: unknown[]) => mockGetLogicalDeviceInfo(...args),
  },
}))

const device: EdgeDevice = {
  id: 7,
  node_id: 'F0F5BDFFFE02',
  channel_id: 3,
  name: 'BMS-1',
  device_type: 'jiabaida_bms',
  protocol: 'modbus',
  hardware_type: 'uart',
  hardware_id: 'UART1',
  config: {},
  status: 'active',
  last_data: null,
  last_data_time: null,
  created_at: '2026-08-01T00:00:00Z',
  logical_device_id: 12,
  node: { id: 1, name: 'Collector-A' },
}

let deferred: { promise: Promise<any>; resolve: (v: any) => void; reject: (e: any) => void }

function makeDeferred() {
  let resolve!: (v: any) => void
  let reject!: (e: any) => void
  const promise = new Promise<any>((res, rej) => {
    resolve = res
    reject = rej
  })
  deferred = { promise, resolve, reject }
  return deferred
}

function mountDialog(props: Record<string, unknown> = {}) {
  return mount(DeviceDeleteDialog, {
    props: { visible: true, device, submitting: false, ...props },
  })
}

describe('DeviceDeleteDialog.vue (方案 v3.3 §2.1)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockGetLogicalDeviceInfo.mockReturnValue(makeDeferred().promise)
  })

  it('打开即显示设备基本信息（本地数据，无延迟）', async () => {
    const wrapper = mountDialog()
    // 信息请求仍 pending，基本信息已经渲染
    expect(wrapper.text()).toContain('BMS-1')
    expect(wrapper.text()).toContain('Collector-A')
    expect(wrapper.text()).toContain('UART UART1')
    expect(wrapper.text()).toContain('jiabaida_bms')
    expect(mockGetLogicalDeviceInfo).toHaveBeenCalledWith(7)
  })

  it('逻辑设备信息加载中显示骨架屏', async () => {
    const wrapper = mountDialog()
    expect(wrapper.find('.logical-info-skeleton').exists()).toBe(true)
    expect(wrapper.find('.logical-info').exists()).toBe(false)
  })

  it('信息加载成功后展示实例数/数据量估算/保留天数，保留项带天数提示', async () => {
    const wrapper = mountDialog()
    deferred.resolve({
      edge_device_id: 7,
      name: 'BMS 逻辑设备',
      logical_device_id: 12,
      retention_days: 30,
      instance_count: 3,
      row_estimate: 1200,
    })
    await flushPromises()

    const text = wrapper.text()
    expect(wrapper.find('.logical-info').exists()).toBe(true)
    expect(text).toContain('BMS 逻辑设备')
    expect(text).toContain('实例数：')
    expect(text).toContain('3')
    expect(text).toContain('约 1,200 条')
    expect(text).toContain('历史数据保留：')
    expect(text).toContain('30 天')
    // radio 保留项文案带 retention_days
    expect(text).toContain('保留 30 天')
  })

  it('估算降级（无 row_estimate）时不显示数据量行', async () => {
    const wrapper = mountDialog()
    deferred.resolve({
      edge_device_id: 7,
      name: null,
      logical_device_id: null,
      retention_days: null,
      instance_count: 1,
    })
    await flushPromises()

    const text = wrapper.text()
    expect(wrapper.find('.logical-info').exists()).toBe(true)
    expect(text).not.toContain('数据量估算')
    expect(text).not.toContain('历史数据保留')
  })

  it('信息请求失败降级为不显示信息区，且不阻塞删除', async () => {
    const wrapper = mountDialog()
    deferred.reject(new Error('boom'))
    await flushPromises()

    expect(wrapper.find('.logical-info').exists()).toBe(false)
    expect(wrapper.find('.logical-info-skeleton').exists()).toBe(false)

    // 仍可提交删除
    await wrapper.find('.el-dialog__footer .el-button--danger').trigger('click')
    expect(wrapper.emitted('confirm')).toBeTruthy()
    expect(wrapper.emitted('confirm')![0]).toEqual([false])
  })

  it('radio 默认选中保留历史数据，确认时 emit confirm(false)', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    expect(radios).toHaveLength(2)
    expect((radios[0].element as HTMLInputElement).checked).toBe(true)
    expect((radios[1].element as HTMLInputElement).checked).toBe(false)

    await wrapper.find('.el-dialog__footer .el-button--danger').trigger('click')
    expect(wrapper.emitted('confirm')).toHaveLength(1)
    expect(wrapper.emitted('confirm')![0]).toEqual([false])
  })

  it('选择"同时删除历史数据"后确认 emit confirm(true)，危险文案可见', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    expect(wrapper.text()).toContain('将在后台删除，不可恢复')

    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    await radios[1].setValue(true)
    await flushPromises()
    expect((radios[1].element as HTMLInputElement).checked).toBe(true)

    await wrapper.find('.el-dialog__footer .el-button--danger').trigger('click')
    expect(wrapper.emitted('confirm')![0]).toEqual([true])
  })

  it('取消按钮关闭弹窗 (update:visible=false)', async () => {
    const wrapper = mountDialog()
    await wrapper.find('.el-dialog__footer .el-button--default').trigger('click')
    expect(wrapper.emitted('update:visible')).toBeTruthy()
    expect(wrapper.emitted('update:visible')![0]).toEqual([false])
  })

  it('关闭后在途的信息请求失效，不污染下次打开', async () => {
    const wrapper = mountDialog()
    // 关闭弹窗（visible=false）
    await wrapper.setProps({ visible: false })
    // 旧请求此时才返回
    deferred.resolve({
      edge_device_id: 7,
      name: 'stale',
      logical_device_id: 12,
      retention_days: 30,
      instance_count: 2,
    })
    await flushPromises()
    // 弹窗已关闭且不显示陈旧信息
    expect(wrapper.find('.logical-info').exists()).toBe(false)
  })

  it('重新打开时 radio 重置为保留、信息重新请求', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    await radios[1].setValue(true)
    deferred.resolve({
      edge_device_id: 7, name: 'x', logical_device_id: 12,
      retention_days: 7, instance_count: 1,
    })
    await flushPromises()

    await wrapper.setProps({ visible: false })
    await wrapper.setProps({ visible: true })
    await flushPromises()

    expect(mockGetLogicalDeviceInfo).toHaveBeenCalledTimes(2)
    const freshRadios = wrapper.findAll('.data-action-group input[type="radio"]')
    expect((freshRadios[0].element as HTMLInputElement).checked).toBe(true)
    expect((freshRadios[1].element as HTMLInputElement).checked).toBe(false)
  })
})
