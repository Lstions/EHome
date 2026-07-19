import client from './client'

export type OperationStatus = 'QUEUED' | 'DISPATCHED' | 'DEVICE_ACCEPTED' | 'VERIFYING' | 'SUCCEEDED' | 'FAILED' | 'UNKNOWN' | 'CANCELLED'
export interface ActionParameter { type: 'string' | 'boolean' | 'integer' | 'number'; minimum?: number; maximum?: number; min_length?: number; max_length?: number; enum?: string[] }
export interface ActionParameterSchema { properties?: Record<string, ActionParameter>; required?: string[] }
export interface ActionDefinition { id: string; version: number; name: string; description: string; device_type: string; semantics: string; risk: string; transport: 'channel_cmd_v2'; input_schema?: ActionParameterSchema }
export interface EffectiveAction { definition: ActionDefinition; available: boolean; reason?: string; reason_code?: string }
export interface VerifiedSensorValue { name: string; value: number; unit?: string; string_value?: string }
export interface ConfirmationGrant { token: string; expires_at: string }
export interface DeviceOperation { command_id: string; edge_device_id: number; node_id: string; action_id: string; action_version: number; status: OperationStatus; final_reason?: string; verified_result?: VerifiedSensorValue[]; created_at: string; updated_at: string; completed_at?: string }
const unwrap = <T>(response: any): T => (response?.data ?? response) as T
export function newIdempotencyKey(): string {
  const cryptoApi = globalThis.crypto as (Crypto & { randomUUID?: () => string }) | undefined
  if (cryptoApi?.randomUUID) return cryptoApi.randomUUID()
  const bytes = new Uint8Array(16)
  if (cryptoApi?.getRandomValues) cryptoApi.getRandomValues(bytes)
  else for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256)
  bytes[6] = (bytes[6] & 0x0f) | 0x40; bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = [...bytes].map(value => value.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
export const deviceOperationApi = {
  actions: async (id: number) => unwrap<EffectiveAction[]>(await client.get(`/api/v1/edge-devices/${id}/actions`)),
  list: async (id: number) => unwrap<DeviceOperation[]>(await client.get(`/api/v1/edge-devices/${id}/operations`)),
  async create(id: number, actionId: string, params: Record<string, unknown> = {}, confirmationToken = '', reason = '', idempotencyKey = newIdempotencyKey()): Promise<DeviceOperation> {
    const request = () => client.post(`/api/v1/edge-devices/${id}/operations`, { action_id: actionId, params, confirmation_token: confirmationToken, reason }, { headers: { 'Idempotency-Key': idempotencyKey } })
    try {
      return unwrap<{ execution: DeviceOperation }>(await request()).execution
    } catch (error: any) {
      // A lost HTTP response is ambiguous: the transaction may already have
      // reached Outbox. Retry the exact same intent/key once; never mint a
      // second physical operation for a transport-level timeout.
      if (!error?.response) return unwrap<{ execution: DeviceOperation }>(await request()).execution
      throw error
    }
  },
  confirm: async (id: number, actionId: string, params: Record<string, unknown>, reason: string) => unwrap<ConfirmationGrant>(await client.post(`/api/v1/edge-devices/${id}/actions/${actionId}/confirm`, { params, reason })),
}
