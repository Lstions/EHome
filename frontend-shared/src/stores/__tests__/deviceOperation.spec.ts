import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { clearSessionCaches } from '@/utils/sessionCache'
import { useDeviceOperationStore } from '../deviceOperation'

const { actions, list, create, confirm, resolve, getDetail, queryResources } = vi.hoisted(() => ({
  actions: vi.fn<(...args: any[]) => Promise<any>>(() => Promise.resolve([])),
  list: vi.fn<(...args: any[]) => Promise<any>>(() => Promise.resolve([])),
  create: vi.fn<(...args: any[]) => Promise<any>>(),
  confirm: vi.fn<(...args: any[]) => Promise<any>>(),
  resolve: vi.fn<(...args: any[]) => Promise<any>>(),
  getDetail: vi.fn<(...args: any[]) => Promise<any>>(),
  queryResources: vi.fn<(...args: any[]) => Promise<any>>(),
}))

vi.mock('@/api/deviceOperation', () => ({
  deviceOperationApi: { actions, list, create, confirm, resolve },
}))
vi.mock('@/api/edgeDevice', () => ({ edgeDeviceApi: { getDetail } }))
vi.mock('@/api/node', () => ({ nodeApi: { queryResources } }))

const operation = (status: any, updatedAt = '2026-07-19T00:00:00Z') => ({
  command_id: 'command-1', edge_device_id: 7, node_id: 'node-1', action_id: 'read_rainfall',
  action_version: 1, status, created_at: '2026-07-19T00:00:00Z', updated_at: updatedAt,
})

const staleCatalog = [{ definition: { id: 'read' }, available: false, reason_code: 'capability_stale' }]

describe('useDeviceOperationStore ordering', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    actions.mockResolvedValue([])
    list.mockResolvedValue([])
    getDetail.mockResolvedValue({ node_id: 'node-1' })
    queryResources.mockResolvedValue({ request_id: 'resource-1' })
  })

  it('does not regress an early WebSocket acceptance or final with a stale HTTP response', () => {
    const store = useDeviceOperationStore()
    store.apply(operation('DEVICE_ACCEPTED'))
    store.apply(operation('QUEUED', '2026-07-19T00:00:01Z'))
    expect(store.histories.get(7)?.[0].status).toBe('DEVICE_ACCEPTED')

    store.apply(operation('SUCCEEDED', '2026-07-19T00:00:02Z'))
    store.apply(operation('DISPATCHED', '2026-07-19T00:00:03Z'))
    expect(store.histories.get(7)?.[0].status).toBe('SUCCEEDED')
  })

  it('merges an additive manual resolution without reopening UNKNOWN', () => {
    const store = useDeviceOperationStore()
    store.apply(operation('UNKNOWN'))
    store.apply({
      ...operation('UNKNOWN', '2026-07-19T00:00:02Z'),
      manual_resolution: { outcome: 'CONFIRMED_FAILED', reason: 'checked on site', resolved_by: 1, resolved_at: '2026-07-19T00:00:02Z' },
    })
    expect(store.histories.get(7)?.[0].manual_resolution?.outcome).toBe('CONFIRMED_FAILED')
    store.apply(operation('DISPATCHED', '2026-07-19T00:00:03Z'))
    expect(store.histories.get(7)?.[0].status).toBe('UNKNOWN')
  })

  it('merges refresh history instead of replacing an early event', async () => {
    const store = useDeviceOperationStore()
    store.apply(operation('DEVICE_ACCEPTED'))
    list.mockResolvedValueOnce([operation('QUEUED')])
    await store.refresh(7)
    expect(store.histories.get(7)?.[0].status).toBe('DEVICE_ACCEPTED')
  })

  it('does not restore a refresh that completed after session clear', async () => {
    let resolveActions!: (value: any) => void
    let resolveList!: (value: any) => void
    actions.mockImplementationOnce(() => new Promise(resolve => { resolveActions = resolve }))
    list.mockImplementationOnce(() => new Promise(resolve => { resolveList = resolve }))
    const store = useDeviceOperationStore()
    const pending = store.refresh(7)
    clearSessionCaches()
    resolveActions([])
    resolveList([operation('SUCCEEDED')])
    await pending
    expect(store.catalogs.size).toBe(0)
    expect(store.histories.size).toBe(0)
  })

  it('forwards a confirmation token and reason when creating a confirmed action', async () => {
    create.mockResolvedValueOnce(operation('QUEUED'))
    const store = useDeviceOperationStore()
    await store.create(7, 'set_mode', { mode: 'SBU' }, 'token-1', 'controlled change')
    expect(create).toHaveBeenCalledWith(7, 'set_mode', { mode: 'SBU' }, 'token-1', 'controlled change')
  })

  it('forwards and applies an UNKNOWN manual resolution', async () => {
    resolve.mockResolvedValueOnce({
      ...operation('UNKNOWN'),
      manual_resolution: { outcome: 'ACKNOWLEDGED_UNKNOWN', reason: 'no independent evidence', resolved_by: 1, resolved_at: '2026-07-19T00:00:02Z' },
    })
    const store = useDeviceOperationStore()
    await store.resolve('command-1', 'ACKNOWLEDGED_UNKNOWN', 'no independent evidence')
    expect(resolve).toHaveBeenCalledWith('command-1', 'ACKNOWLEDGED_UNKNOWN', 'no independent evidence')
    expect(store.histories.get(7)?.[0].manual_resolution?.outcome).toBe('ACKNOWLEDGED_UNKNOWN')
  })

  it('refreshes a stale resource snapshot once before reloading the action catalog', async () => {
    vi.useFakeTimers()
    actions
      .mockResolvedValueOnce(staleCatalog)
      .mockResolvedValueOnce([{ definition: { id: 'read' }, available: true }])
    const store = useDeviceOperationStore()
    const pending = store.refresh(7)
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(2000)
    await pending
    expect(getDetail).toHaveBeenCalledWith(7)
    expect(queryResources).toHaveBeenCalledWith('node-1')
    expect(actions).toHaveBeenCalledTimes(2)
    expect(store.catalogs.get(7)?.[0].available).toBe(true)
    vi.useRealTimers()
  })
})

/**
 * P0-b（2026-09-21）：capability_stale 的**用户可行动指引**与**点击时自愈**。
 *
 * 缺陷形态（生产实测）：GET /edge-devices/1/actions 返回 9 个操作
 * available=false / reason='ChannelCmdV2 capability is unavailable or stale'，
 * 界面只有一行「暂不可用操作（9）」，用户既不知道为什么，也没有任何可点的入口。
 * 而且自愈**只发生在页面加载时** —— 一个开着页面不动的用户永远等不到第二次机会。
 *
 * 这组用例锁定两件事：
 *   1. 自愈之后仍 stale 时，store 必须留下可被 UI 判定的证据（lastRefresh）；
 *   2. retryCapabilityRefresh 是**独立于 refresh 的点击路径**，它自己也必须下发
 *      QueryResources（不能只是重新读一次已经过期的目录）。
 */
describe('useDeviceOperationStore capability_stale 可行动指引（P0-b）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    actions.mockResolvedValue([])
    list.mockResolvedValue([])
    getDetail.mockResolvedValue({ node_id: 'node-1' })
    queryResources.mockResolvedValue({ request_id: 'resource-1' })
  })

  it('自动刷新后仍然 stale：lastRefresh 必须记录 attempted=true 且 recovered=false', async () => {
    vi.useFakeTimers()
    actions.mockResolvedValue(staleCatalog)
    const store = useDeviceOperationStore()
    const pending = store.refresh(7)
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(2000)
    await pending
    const attempt = store.lastRefresh.get(7)
    expect(attempt?.attempted).toBe(true)
    expect(attempt?.recovered).toBe(false)
    expect(attempt?.error).toBe('')
    expect(attempt?.nodeId).toBe('node-1')
    vi.useRealTimers()
  })

  it('点击「立即恢复」必须**再次**下发 QueryResources 并重取目录（不能只重读缓存）', async () => {
    vi.useFakeTimers()
    actions.mockResolvedValue(staleCatalog)
    const store = useDeviceOperationStore()
    const pending = store.retryCapabilityRefresh(7)
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(2000)
    await pending
    expect(queryResources).toHaveBeenCalledWith('node-1')
    // 目录被重取：初次 + 自愈后各一次。
    expect(actions.mock.calls.length).toBeGreaterThanOrEqual(2)
    vi.useRealTimers()
  })

  it('点击「立即恢复」在节点恢复上报后，目录必须变为可用且 recovered=true', async () => {
    vi.useFakeTimers()
    actions
      .mockResolvedValueOnce(staleCatalog)
      .mockResolvedValueOnce([{ definition: { id: 'read' }, available: true }])
    const store = useDeviceOperationStore()
    const pending = store.retryCapabilityRefresh(7)
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(2000)
    await pending
    expect(store.lastRefresh.get(7)?.recovered).toBe(true)
    expect(store.catalogs.get(7)?.[0].available).toBe(true)
    vi.useRealTimers()
  })

  it('节点未回应时必须留下可行动文案所需的原因（不能静默当成成功）', async () => {
    vi.useFakeTimers()
    actions.mockResolvedValue(staleCatalog)
    queryResources.mockRejectedValueOnce(new Error('节点离线'))
    const store = useDeviceOperationStore()
    const pending = store.retryCapabilityRefresh(7)
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(2000)
    await pending
    const attempt = store.lastRefresh.get(7)
    expect(attempt?.attempted).toBe(true)
    expect(attempt?.error).toBe('节点离线')
    expect(attempt?.recovered).toBe(false)
    // 原始（不可用）目录仍然是权威结果，不得被清空成「可用」。
    expect(store.catalogs.get(7)?.[0].available).toBe(false)
    vi.useRealTimers()
  })

  it('边缘设备没有关联节点时必须区分「没尝试」与「尝试失败」', async () => {
    vi.useFakeTimers()
    actions.mockResolvedValue(staleCatalog)
    getDetail.mockResolvedValueOnce({ node_id: '' })
    const store = useDeviceOperationStore()
    const pending = store.retryCapabilityRefresh(7)
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(2000)
    await pending
    const attempt = store.lastRefresh.get(7)
    expect(attempt?.attempted).toBe(false)
    expect(attempt?.error).toBe('该边缘设备未关联节点')
    expect(queryResources).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('目录本来新鲜时不得打扰节点（无谓的 QueryResources 是噪声）', async () => {
    actions.mockResolvedValue([{ definition: { id: 'read' }, available: true }])
    const store = useDeviceOperationStore()
    await store.refresh(7)
    expect(queryResources).not.toHaveBeenCalled()
    expect(store.lastRefresh.get(7)?.attempted).toBe(false)
  })

  it('会话清理必须同时清掉指引状态（否则换账号后会看到上一个会话的文案）', async () => {
    vi.useFakeTimers()
    actions.mockResolvedValue(staleCatalog)
    const store = useDeviceOperationStore()
    const pending = store.refresh(7)
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(2000)
    await pending
    expect(store.lastRefresh.size).toBe(1)
    clearSessionCaches()
    expect(store.lastRefresh.size).toBe(0)
    expect(store.catalogs.size).toBe(0)
    vi.useRealTimers()
  })
})
