import client from './client'

export type OperationStatus = 'QUEUED' | 'DISPATCHED' | 'DEVICE_ACCEPTED' | 'VERIFYING' | 'SUCCEEDED' | 'FAILED' | 'UNKNOWN' | 'CANCELLED'
export interface ActionParameter { type: 'string' | 'boolean' | 'integer' | 'number'; minimum?: number; maximum?: number; min_length?: number; max_length?: number; enum?: string[] }
export interface ActionParameterSchema { properties?: Record<string, ActionParameter>; required?: string[] }
export interface ActionDefinition { id: string; version: number; name: string; description: string; device_type: string; semantics: string; risk: string; transport: 'channel_cmd_v2'; input_schema?: ActionParameterSchema }
export interface EffectiveAction { definition: ActionDefinition; available: boolean; reason?: string }
export interface VerifiedSensorValue { name: string; value: number; unit?: string; string_value?: string }
export interface ConfirmationGrant { token: string; expires_at: string }
export interface DeviceOperation { command_id: string; edge_device_id: number; node_id: string; action_id: string; action_version: number; status: OperationStatus; final_reason?: string; verified_result?: VerifiedSensorValue[]; created_at: string; updated_at: string; completed_at?: string }
const unwrap = <T>(response: any): T => (response?.data ?? response) as T
export const deviceOperationApi = {
  actions: async (id: number) => unwrap<EffectiveAction[]>(await client.get(`/api/v1/edge-devices/${id}/actions`)),
  list: async (id: number) => unwrap<DeviceOperation[]>(await client.get(`/api/v1/edge-devices/${id}/operations`)),
  async create(id: number, actionId: string, params: Record<string, unknown> = {}, confirmationToken = '', reason = ''): Promise<DeviceOperation> {
    const response = unwrap<{ execution: DeviceOperation }>(await client.post(`/api/v1/edge-devices/${id}/operations`, { action_id: actionId, params, confirmation_token: confirmationToken, reason }, { headers: { 'Idempotency-Key': crypto.randomUUID() } }))
    return response.execution
  },
  confirm: async (id: number, actionId: string, params: Record<string, unknown>, reason: string) => unwrap<ConfirmationGrant>(await client.post(`/api/v1/edge-devices/${id}/actions/${actionId}/confirm`, { params, reason })),
}
