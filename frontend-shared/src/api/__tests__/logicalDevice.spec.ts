import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockClient = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  delete: vi.fn(),
  defaults: { baseURL: '' },
}))

vi.mock('../client', () => ({
  default: mockClient,
}))

import { logicalDeviceApi, extractMergeConflicts } from '../logicalDevice'

describe('logicalDeviceApi', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('list unwraps { items, total } envelope', async () => {
    const items = [{ id: 1, name: 'LD-1', device_type: 'bms', retention_days: 365, instance_count: 2 }]
    mockClient.get.mockResolvedValue({ data: { items, total: 1 } })
    const res = await logicalDeviceApi.list()
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/logical-devices')
    expect(res.items).toEqual(items)
    expect(res.total).toBe(1)
  })

  it('list tolerates bare-array responses', async () => {
    mockClient.get.mockResolvedValue({ data: [{ id: 2, name: 'X' }] })
    const res = await logicalDeviceApi.list()
    // 裸数组无 items 字段 → 空列表, 不抛错 (降级语义)
    expect(res.items).toEqual([])
  })

  it('mergePreview posts source_ids', async () => {
    const preview = {
      sources: [
        { id: 3, name: 'A', device_type: 'bms', first_data_at: null, last_data_at: null, overlap_with_others: false },
      ],
      target_retention_days: 365,
    }
    mockClient.post.mockResolvedValue({ data: preview })
    const res = await logicalDeviceApi.mergePreview([3, 5])
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/logical-devices/merge/preview', { source_ids: [3, 5] })
    expect(res.target_retention_days).toBe(365)
  })

  it('merge posts target_name + source_ids and unwraps job ids', async () => {
    mockClient.post.mockResolvedValue({ data: { target_id: 9, job_ids: [11, 12] } })
    const res = await logicalDeviceApi.merge('客厅BMS', [3, 5])
    expect(mockClient.post).toHaveBeenCalledWith('/api/v1/logical-devices/merge', {
      target_name: '客厅BMS',
      source_ids: [3, 5],
    })
    expect(res).toEqual({ target_id: 9, job_ids: [11, 12] })
  })

  it('mergeJob gets progress by id', async () => {
    const job = { id: 11, status: 'running', migrated_rows: 100, total_estimate: 200 }
    mockClient.get.mockResolvedValue({ data: job })
    const res = await logicalDeviceApi.mergeJob(11)
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/logical-devices/merge-jobs/11')
    expect(res.status).toBe('running')
  })

  it('update puts name/retention_days', async () => {
    mockClient.put.mockResolvedValue({ data: { id: 3, name: '新名', retention_days: 730 } })
    const res = await logicalDeviceApi.update(3, { name: '新名', retention_days: 730 })
    expect(mockClient.put).toHaveBeenCalledWith('/api/v1/logical-devices/3', { name: '新名', retention_days: 730 })
    expect(res.retention_days).toBe(730)
  })
})

describe('extractMergeConflicts', () => {
  it('extracts structured conflicts from 409 error response', () => {
    const error = {
      status: 409,
      response: {
        data: {
          code: 409,
          message: '合并校验未通过',
          conflicts: [
            { logical_device_id: 3, logical_name: '客厅BMS', reason: 'alive_instance', instance_id: 17, instance_name: 'BMS-1', node_name: '客厅采集器' },
            { logical_device_id: 5, logical_name: '卧室BMS', reason: 'purge_requested' },
          ],
        },
      },
    }
    const conflicts = extractMergeConflicts(error)
    expect(conflicts).toHaveLength(2)
    expect(conflicts[0].instance_id).toBe(17)
    expect(conflicts[1].reason).toBe('purge_requested')
  })

  it('returns [] for errors without conflicts payload', () => {
    expect(extractMergeConflicts(new Error('network'))).toEqual([])
    expect(extractMergeConflicts({ response: { data: { message: 'x' } } })).toEqual([])
    expect(extractMergeConflicts(null)).toEqual([])
  })
})
