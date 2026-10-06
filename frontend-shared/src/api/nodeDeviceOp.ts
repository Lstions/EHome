import client from './client'

// ApiResponse mirrors the shape the response interceptor leaves on `response.data`
// (the interceptor unwraps the transport envelope). It is declared the same way
// node.ts does it -- client.get<unknown, ApiResponse<T>> -- because that is the
// convention in this codebase, and a second, subtly different unwrapping style
// is how two callers end up disagreeing about what `data` contains.
interface ApiResponse<T> {
  data: T
}

// nodeDeviceOp.ts -- node-level reboot / factory reset.
//
// # Why this is separate from deviceOperation.ts
//
// deviceOperation.ts drives EDGE-device actions through the commandexec
// pipeline (channel commands, manifests, verification). A COLLECTOR reboot has
// no channel, no manifest and no edge-device id; it is a different operation on
// a different kind of target. Sharing one client module would mean one module
// whose parameters mean different things depending on which endpoint it called.
//
// # The timeout is longer than the client default, on purpose
//
// The backend waits up to 15s for the device's ACK and then answers 202
// ("delivered, not acknowledged -- the outcome is UNKNOWN"). The axios default
// is 10s, so with the default the browser would abort at 10s and show a generic
// network error -- the carefully-distinguished 202 would NEVER reach the user,
// and an operator would be left with "it failed" when the truth is "we do not
// know". Hence an explicit 20s here: strictly longer than the server's own
// wait, so the server's answer is always the one the user sees.

const DEVICE_OP_TIMEOUT_MS = 20000

export type NodeDeviceOp = 'reboot' | 'factory_reset'

export interface NodeDeviceOpOption {
  id: NodeDeviceOp
  name: string
  description: string
}

export interface NodeDeviceOpResult {
  request_id: string
  op: NodeDeviceOp
  /** true = the device answered. false = no answer; the outcome is UNKNOWN. */
  acked: boolean
  /** Human-readable device result, e.g. "ok", "erase_failed", "unrecognized(9)". */
  result: string
  detail?: string
}

export interface NodeDeviceOpCatalog {
  /** false = this server has no device transport; the UI must not offer the buttons. */
  supported: boolean
  operations: NodeDeviceOpOption[]
}

export const nodeDeviceOpApi = {
  /** Supported operations, so the UI never offers one the server cannot do. */
  async catalog(): Promise<NodeDeviceOpCatalog> {
    const response = await client.get<unknown, ApiResponse<NodeDeviceOpCatalog>>(
      '/api/v1/nodes/device-ops',
      { timeout: DEVICE_OP_TIMEOUT_MS },
    )
    return response.data
  },

  /**
   * Ask a node to reboot or factory-reset itself.
   *
   * NOTE the shape of what this returns: `acked: false` is a SUCCESSFUL call
   * with an unknown outcome, not an error. The backend answers 202 in that case
   * and axios treats 2xx as success, so this resolves rather than throwing --
   * callers must look at `acked`, not just at whether it threw.
   *
   * A device REFUSAL throws (HTTP 409 with errorCode `device_rejected:<reason>`).
   */
  async run(id: number | string, op: NodeDeviceOp, reason?: string): Promise<NodeDeviceOpResult> {
    const response = await client.post<unknown, ApiResponse<NodeDeviceOpResult>>(
      `/api/v1/nodes/${id}/device-ops`,
      { op, ...(reason ? { reason } : {}) },
      { timeout: DEVICE_OP_TIMEOUT_MS },
    )
    return response.data
  },
}

export { DEVICE_OP_TIMEOUT_MS }
