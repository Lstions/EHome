import { beforeEach, describe, expect, it, vi } from 'vitest'

const mockClient = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('../client', () => ({ default: mockClient }))

import {
  DEVICE_OP_TIMEOUT_MS,
  nodeDeviceOpApi,
  type NodeDeviceOpResult,
} from '../nodeDeviceOp'
import client from '../client'

// nodeDeviceOp.spec.ts -- the client for reboot / factory reset.
//
// Two properties carry real risk here and are tested explicitly:
//
//  1) The call timeout must be LONGER than the server's own wait for the ACK.
//     The axios default is 10s while the server waits up to 15s, so with the
//     default the browser aborts first and the carefully-distinguished
//     202 ("outcome unknown") never reaches the operator -- they would see a
//     generic network error and conclude the device is fine/broken, both wrong.
//
//  2) acked=false must resolve, not throw. It means "we do not know", which is
//     a successful request with an unknown outcome, not a failure.

describe('nodeDeviceOpApi', () => {
  beforeEach(() => vi.clearAllMocks())

  it('reads the operation catalog', async () => {
    mockClient.get.mockResolvedValue({
      data: {
        supported: true,
        operations: [
          { id: 'reboot', name: '重启', description: 'restart' },
          { id: 'factory_reset', name: '恢复出厂', description: 'wipe config' },
        ],
      },
    })

    const catalog = await nodeDeviceOpApi.catalog()

    expect(catalog.supported).toBe(true)
    expect(catalog.operations.map(op => op.id)).toEqual(['reboot', 'factory_reset'])
    expect(mockClient.get).toHaveBeenCalledWith('/api/v1/nodes/device-ops', {
      timeout: DEVICE_OP_TIMEOUT_MS,
    })
  })

  it('reports a server with no device transport as unsupported', async () => {
    // The UI must be able to tell "this server cannot do it" from "it failed".
    mockClient.get.mockResolvedValue({ data: { supported: false, operations: [] } })

    const catalog = await nodeDeviceOpApi.catalog()

    expect(catalog.supported).toBe(false)
  })

  it('posts the op and returns the device outcome', async () => {
    const result: NodeDeviceOpResult = {
      request_id: 'op-node-1-123-1',
      op: 'reboot',
      acked: true,
      result: 'ok',
    }
    mockClient.post.mockResolvedValue({ data: result })

    const got = await nodeDeviceOpApi.run('node-1', 'reboot')

    expect(got).toEqual(result)
    expect(mockClient.post).toHaveBeenCalledWith(
      '/api/v1/nodes/node-1/device-ops',
      { op: 'reboot' },
      { timeout: DEVICE_OP_TIMEOUT_MS },
    )
  })

  it('sends the reason when one is given', async () => {
    mockClient.post.mockResolvedValue({
      data: { request_id: 'r', op: 'reboot', acked: true, result: 'ok' },
    })

    await nodeDeviceOpApi.run(7, 'reboot', '运维巡检')

    expect(mockClient.post).toHaveBeenCalledWith(
      '/api/v1/nodes/7/device-ops',
      { op: 'reboot', reason: '运维巡检' },
      { timeout: DEVICE_OP_TIMEOUT_MS },
    )
  })

  it('RESOLVES (does not throw) when the device never acknowledged', async () => {
    // The backend answers 202; axios treats 2xx as success. This must be a
    // resolved value with acked=false, because "no answer" is not a failure and
    // the caller has to be able to tell the operator "it may have worked".
    mockClient.post.mockResolvedValue({
      data: { request_id: 'r', op: 'reboot', acked: false, result: 'ok' },
    })

    const got = await nodeDeviceOpApi.run('node-1', 'reboot')

    expect(got.acked).toBe(false)
  })

  it('uses a timeout strictly longer than the server wait', () => {
    // nodemgr.DefaultDeviceOpTimeout is 15s. If the client ever drops to or
    // below that, the browser gives up BEFORE the server answers and the 202
    // path becomes unreachable in production.
    expect(DEVICE_OP_TIMEOUT_MS).toBeGreaterThan(15_000)
  })

  it('passes the same explicit timeout on both calls', async () => {
    mockClient.get.mockResolvedValue({ data: { supported: true, operations: [] } })
    mockClient.post.mockResolvedValue({
      data: { request_id: 'r', op: 'reboot', acked: true, result: 'ok' },
    })

    await nodeDeviceOpApi.catalog()
    await nodeDeviceOpApi.run('n', 'reboot')

    const getTimeout = mockClient.get.mock.calls[0][1].timeout
    const postTimeout = mockClient.post.mock.calls[0][2].timeout
    expect(getTimeout).toBe(DEVICE_OP_TIMEOUT_MS)
    expect(postTimeout).toBe(DEVICE_OP_TIMEOUT_MS)
  })

  it('keeps the two operations distinct on the wire', async () => {
    mockClient.post.mockResolvedValue({
      data: { request_id: 'r', op: 'factory_reset', acked: true, result: 'ok' },
    })

    await nodeDeviceOpApi.run('n', 'factory_reset')

    expect(mockClient.post.mock.calls[0][1]).toEqual({ op: 'factory_reset' })
    // and the default client instance is what we used
    expect(client).toBe(mockClient)
  })
})
