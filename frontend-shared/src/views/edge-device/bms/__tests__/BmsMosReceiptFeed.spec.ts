import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useDeviceOperationStore } from '@/stores/deviceOperation'
import { useBmsMosControl } from '../composables/useBmsMosControl'
import { useBmsOperations } from '../composables/useBmsOperations'
import type { DeviceOperation } from '@/api/deviceOperation'

/**
 * 2026-11-17 真机缺陷回归（BMS MOS 卡片）。
 *
 * 用户报障两件事：
 *   ① 把 MOS 打开/关闭后卡片一直显示「已发送…」，开关期间一直是 disabled，
 *      **必须手点页头「刷新数据」**才恢复（回执到不了页面，见 useDeviceOperationFeed 的用例）；
 *   ② 开关会「忽然跳一下打开、再跳回关闭」——写后回读已经确认了新状态，
 *      却被一帧**更旧**的周期帧打回旧值，下一帧再跳回来。
 *
 * 这里钉住本域的三条不变量：
 *   · 终态回执到达 ⇒ 在途文案撤掉 + 用回读值桥接显示；
 *   · 回执始终不到 ⇒ 看门狗到点撤在途态并如实告知（绝不永挂、绝不谎报成功）；
 *   · 桥接只在"没有比回读更新的帧"时有效 ⇒ 旧帧打不回来、新帧一定生效。
 */

vi.mock('@/utils/feedback', () => ({
  feedback: {
    confirmDanger: vi.fn(async () => true),
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
  extractErrorMessage: (_e: unknown, fallback: string) => fallback,
}))

function makeOp(overrides: Partial<DeviceOperation> = {}): DeviceOperation {
  return {
    command_id: 'cmd-1',
    edge_device_id: 7,
    node_id: 'F0F5BD02DD84',
    action_id: 'set_mos_policy',
    action_version: 1,
    status: 'QUEUED',
    created_at: '2026-11-17T09:59:59Z',
    updated_at: '2026-11-17T10:00:00Z',
    completed_at: '2026-11-17T10:00:00Z',
    ...overrides,
  } as DeviceOperation
}

function makeDeps(overrides: Partial<Parameters<typeof useBmsMosControl>[0]> = {}) {
  return {
    deviceId: ref(7),
    deviceOnline: ref(true),
    // 真机现场：fet_status=0x02（充电关断、放电导通）
    latestData: ref<Record<string, any> | null>({ fet_status: 2 }),
    dispatch: vi.fn(async () => true),
    clearError: vi.fn(),
    lastError: ref(''),
    fieldReportedAt: ref<Record<string, number>>({}),
    trackCommand: vi.fn(),
    ...overrides,
  }
}

describe('useBmsMosControl 回执/看门狗/桥接', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('下发后进入在途态，终态回执到达即撤掉并桥接到回读值', async () => {
    const deps = makeDeps()
    const mos = useBmsMosControl(deps)

    await mos.requestMosChange('charge', true)
    expect(deps.dispatch).toHaveBeenCalledTimes(1)
    expect(mos.mosPhase.charge).toBe('已发送')
    expect(deps.trackCommand).toHaveBeenCalledWith('set_mos_policy')

    // 回读确认 bit0 已导通（0x03），但周期帧还停在旧值 0x02
    mos.handleReceipt(makeOp({
      status: 'SUCCEEDED',
      verified_result: [{ name: 'fet_status', value: 3, unit: 'bitmask' }],
    }))
    expect(mos.mosPhase.charge).toBe('')
    expect(mos.chargeOn.value).toBe(true)
  })

  it('回执始终不到：看门狗到点撤掉在途态，并如实说明"没拿到结果"', async () => {
    const deps = makeDeps()
    const mos = useBmsMosControl(deps)

    await mos.requestMosChange('charge', true)
    expect(mos.mosPhase.charge).toBe('已发送')

    await vi.advanceTimersByTimeAsync(15_000)

    // 不能永挂：开关必须恢复可操作
    expect(mos.mosPhase.charge).toBe('')
    // 也不能谎报成功/失败：指令很可能已生效
    expect(mos.mosError.charge).toContain('未收到执行结果')
  })

  it('看门狗不误伤：终态回执已到达时超时不做任何事', async () => {
    const deps = makeDeps()
    const mos = useBmsMosControl(deps)

    await mos.requestMosChange('charge', true)
    mos.handleReceipt(makeOp({ status: 'FAILED', final_reason: 'final_failed' }))
    expect(mos.mosPhase.charge).toBe('')
    const reasonAfterReceipt = mos.mosError.charge

    await vi.advanceTimersByTimeAsync(20_000)
    expect(mos.mosPhase.charge).toBe('')
    expect(mos.mosError.charge).toBe(reasonAfterReceipt)
  })

  it('桥接有效期：比回读更旧的帧不得覆盖（否则开关会跳回旧值）', async () => {
    const deps = makeDeps()
    const mos = useBmsMosControl(deps)

    await mos.requestMosChange('charge', true)
    mos.handleReceipt(makeOp({
      status: 'SUCCEEDED',
      verified_result: [{ name: 'fet_status', value: 3, unit: 'bitmask' }],
    }))
    expect(mos.chargeOn.value).toBe(true)

    // 一帧"写之前采集、写之后才到"的周期帧：采集时刻早于回读 ⇒ 不得覆盖
    deps.fieldReportedAt.value = { fet_status: Date.parse('2026-11-17T09:59:58Z') }
    expect(mos.chargeOn.value).toBe(true)

    // 不含 fet_status 的帧（0xAA 保护次数等）到了 ⇒ 与 MOS 状态无关，仍不得覆盖
    deps.fieldReportedAt.value = { ...deps.fieldReportedAt.value, protection_status: Date.parse('2026-11-17T10:00:05Z') }
    expect(mos.chargeOn.value).toBe(true)

    // 确实晚于回读的 fet_status 上报 ⇒ 以实时值为准（live wins，保护关断也能看见）
    deps.fieldReportedAt.value = { ...deps.fieldReportedAt.value, fet_status: Date.parse('2026-11-17T10:00:01Z') }
    expect(mos.chargeOn.value).toBe(false)
  })

  it('设备切换复位：清在途态、清桥接、清看门狗', async () => {
    const deps = makeDeps()
    const mos = useBmsMosControl(deps)

    await mos.requestMosChange('charge', true)
    mos.handleReceipt(makeOp({
      status: 'SUCCEEDED',
      verified_result: [{ name: 'fet_status', value: 3, unit: 'bitmask' }],
    }))
    expect(mos.chargeOn.value).toBe(true)

    mos.resetMos()
    expect(mos.mosPhase.charge).toBe('')
    expect(mos.mosError.charge).toBe('')
    // 桥接清掉后回到实时值 0x02
    expect(mos.chargeOn.value).toBe(false)
  })
})

describe('useBmsOperations 跟踪登记', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('trackCommand 用 command_id 登记跟踪（回执由 feed 推进）', () => {
    const store = useDeviceOperationStore()
    store.apply(makeOp({ command_id: 'cmd-9', status: 'QUEUED' }))
    const followCommand = vi.fn()

    const ops = useBmsOperations({
      deviceId: ref(7),
      isMobileViewport: ref(false),
      dispatch: vi.fn(async () => true),
      clearError: vi.fn(),
      lastError: ref(''),
      onMosReceipt: vi.fn(),
      followCommand,
    })

    ops.trackCommand('set_mos_policy')
    expect(followCommand).toHaveBeenCalledWith('cmd-9')
  })
})
