import { beforeEach, describe, expect, expectTypeOf, it, vi } from 'vitest'

const mockClient = vi.hoisted(() => ({
  get: vi.fn(),
  delete: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
}))

vi.mock('../client', () => ({ default: mockClient }))

import {
  nodeApi,
  type NodeLogEntry,
  type NodeLogPage,
  type NodeLogQuery,
} from '../node'

const entry: NodeLogEntry = {
  id: 17,
  node_id: 'ESP32-C6-01',
  level: 2,
  ts: 3_661_002_003,
  tag: 'MQTT',
  message: 'connected',
  created_at: '2026-07-13T08:00:00Z',
}

const page: NodeLogPage = {
  total: 1,
  page: 2,
  size: 25,
  logs: [entry],
}

describe('nodeApi log history', () => {
  beforeEach(() => vi.clearAllMocks())

  it('forwards the complete single-level query and unwraps an API envelope', async () => {
    const query: NodeLogQuery = {
      from: 1_700_000_000_000,
      to: '2026-07-13T09:00:00Z',
      level: 2,
      tag: 'MQTT',
      q: 'connect',
      page: 2,
      size: 25,
    }
    mockClient.get.mockResolvedValue({ code: 200, message: 'ok', data: page })

    const result = await nodeApi.getNodeLogs('collector-1', query)

    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/nodes/collector-1/logs', { params: query })
    expect(result).toEqual(page)
    expectTypeOf(result).toEqualTypeOf<NodeLogPage>()
    expectTypeOf(result.logs).toEqualTypeOf<NodeLogEntry[]>()
  })

  it('does not expose the delete-only before parameter on GET queries', () => {
    expectTypeOf<NodeLogQuery>().not.toHaveProperty('before')
  })

  it('accepts the backend bare page response', async () => {
    mockClient.get.mockResolvedValue(page)

    await expect(nodeApi.getNodeLogs(9, { page: 2, size: 25 })).resolves.toEqual(page)
  })

  it('preserves a zero before timestamp when deleting', async () => {
    mockClient.delete.mockResolvedValue({ data: { deleted: 3 } })

    await expect(nodeApi.deleteNodeLogs('collector-1', 0)).resolves.toEqual({ deleted: 3 })
    expect(mockClient.delete).toHaveBeenCalledWith('/api/v1/nodes/collector-1/logs', {
      params: { before: 0 },
    })
  })
})
