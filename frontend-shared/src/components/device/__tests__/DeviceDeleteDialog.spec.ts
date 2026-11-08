import { describe, expect, it, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import DeviceDeleteDialog from '@/components/device/DeviceDeleteDialog.vue'
import type { EdgeDevice } from '@/api/edgeDevice'

// 方案 v3.3 §2.1 单删弹窗: 基本信息即时显示 / 数据量估算异步加载降级 /
// radio 默认保留 / delete_data 两条提交路径。
//
// 2026-10-23 逻辑设备移除（契约 §3/§4.5）：原「逻辑设备信息区」（实例数 /
// 逻辑保留天数）改为「本设备历史数据」（数据量估算 + 系统级保留期）。
// 数据来源从 /edge-devices/:id/logical-device-info 换成
// /edge-devices/:id/data-summary（本设备作为唯一 source）。

const mockGetDataSummary = vi.fn()

vi.mock('@/api/edgeDevice', () => ({
  edgeDeviceApi: {
    getDataSummary: (...args: unknown[]) => mockGetDataSummary(...args),
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
    mockGetDataSummary.mockReturnValue(makeDeferred().promise)
  })

  it('打开即显示设备基本信息（本地数据，无延迟）', async () => {
    const wrapper = mountDialog()
    // 信息请求仍 pending，基本信息已经渲染
    expect(wrapper.text()).toContain('BMS-1')
    expect(wrapper.text()).toContain('Collector-A')
    // 该 fixture 的 hardware_id='UART1' 是历史写法（真实边缘设备上是 Modbus 地址）；
    // 「通道」栏现在读关联通道的总线名，这个 fixture 没有 channel → 回退 '—'。
    // 总线名的完整覆盖见 __tests__/DeviceDeleteDialogChannel.spec.ts。
    expect(wrapper.text()).toContain('—')
    expect(wrapper.text()).toContain('jiabaida_bms')
    // 本设备作为唯一 source 查自己的数据量
    expect(mockGetDataSummary).toHaveBeenCalledWith(7, [7])
  })

  it('数据量加载中显示骨架屏', async () => {
    const wrapper = mountDialog()
    expect(wrapper.find('.data-info-skeleton').exists()).toBe(true)
    expect(wrapper.find('.data-info').exists()).toBe(false)
  })

  it('加载成功后展示数据量估算与系统级保留期（新契约读 target_rows）', async () => {
    const wrapper = mountDialog()
    deferred.resolve({
      target: { source_id: 7, rows: 1200, first_time: null, last_time: null, state: 'live' },
      sources: [],
      target_rows: 1200,
    })
    await flushPromises()

    const text = wrapper.text()
    expect(wrapper.find('.data-info').exists()).toBe(true)
    expect(text).toContain('约 1,200 条')
    expect(text).toContain('系统级')
    // 逻辑设备概念已移除：不得再出现旧文案
    expect(text).not.toContain('逻辑设备')
    expect(text).not.toContain('实例数')
  })

  it('估算降级（target_rows 缺失且 target.rows 为 null）时不显示数据量区，不伪造 0 条', async () => {
    const wrapper = mountDialog()
    deferred.resolve({ target: null, sources: [] })
    await flushPromises()

    expect(wrapper.find('.data-info').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('约 0 条')
  })

  // 真实的降级形状（2026-10-24 审核发现原用例用「空数组」绕开了它）：
  // 后端在**任一来源估算失败**时会省略 total_rows，但 items 仍可能有行
  // （且该行的 rows 为 null）。这才是「估不出总量」的真实响应形状 ——
  // 必须显示「不可用」，绝不能渲染成「约 0 条」（用户会以为没有数据可删）。
  it('真实降级形状（target.rows=null 且无 target_rows）时不显示数据量区', async () => {
    const wrapper = mountDialog()
    deferred.resolve({
      target: { source_id: 7, rows: null, first_time: null, last_time: null, state: 'live' },
      sources: [],
    })
    await flushPromises()

    expect(wrapper.find('.data-info').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('约 0 条')
  })

  it('请求失败降级为不显示信息区，且不阻塞删除', async () => {
    const wrapper = mountDialog()
    deferred.reject(new Error('boom'))
    await flushPromises()

    expect(wrapper.find('.data-info').exists()).toBe(false)
    expect(wrapper.find('.data-info-skeleton').exists()).toBe(false)

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

    // 2026-10-25 文案修正：区分「删除设备行（软删可恢复）」与「删除数据（清理开始后不可恢复）」
    expect(wrapper.text()).toContain('后台分批清理；清理开始后不可恢复')
    expect(wrapper.text()).toContain('恢复已删除设备')

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
      target: { source_id: 7, rows: 999, first_time: null, last_time: null, state: 'live' },
      sources: [],
      target_rows: 999,
    })
    await flushPromises()
    // 弹窗已关闭且不显示陈旧信息
    expect(wrapper.find('.data-info').exists()).toBe(false)
  })

  it('重新打开时 radio 重置为保留、数据量重新请求', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    const radios = wrapper.findAll('.data-action-group input[type="radio"]')
    await radios[1].setValue(true)
    deferred.resolve({
      target: { source_id: 7, rows: 7, first_time: null, last_time: null, state: 'live' },
      sources: [],
      target_rows: 7,
    })
    await flushPromises()

    await wrapper.setProps({ visible: false })
    await wrapper.setProps({ visible: true })
    await flushPromises()

    expect(mockGetDataSummary).toHaveBeenCalledTimes(2)
    const freshRadios = wrapper.findAll('.data-action-group input[type="radio"]')
    expect((freshRadios[0].element as HTMLInputElement).checked).toBe(true)
    expect((freshRadios[1].element as HTMLInputElement).checked).toBe(false)
  })
})
