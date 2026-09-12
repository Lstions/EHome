// src/api/periph.ts — GPIO + PWM 外设控制 API
import client, { type ApiEnvelope } from './client'
export interface CommandAck { request_id: number }

// === GPIO ===

export interface GPIOConfig {
  id?: number
  node_id: string
  pin: number
  direction: number   // 0=INPUT, 1=OUTPUT, 2=INPUT_PULLUP, 3=INPUT_PULLDOWN
  initial_level: number
  label: string
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export const gpioApi = {
  async list(nodeId: string): Promise<GPIOConfig[]> {
    const resp = await client.get<unknown, ApiEnvelope<GPIOConfig[]>>(`/api/v1/nodes/${nodeId}/gpio`)
    if (resp.code && resp.code >= 400) throw new Error('获取 GPIO 配置失败')
    return resp.data ?? []
  },

  async create(nodeId: string, data: Partial<GPIOConfig>): Promise<GPIOConfig> {
    const resp = await client.post<unknown, ApiEnvelope<GPIOConfig>>(`/api/v1/nodes/${nodeId}/gpio`, data)
    return resp.data
  },

  async update(nodeId: string, pin: number, data: Partial<GPIOConfig>): Promise<void> {
    await client.put(`/api/v1/nodes/${nodeId}/gpio/${pin}`, data)
  },

  async delete(nodeId: string, pin: number): Promise<void> {
    await client.delete(`/api/v1/nodes/${nodeId}/gpio/${pin}`)
  },

  async set(nodeId: string, pin: number, level: 0 | 1): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/gpio/${pin}/set`, { level })
    return resp?.data ?? { request_id: 0 }
  },

  async toggle(nodeId: string, pin: number): Promise<void> {
    await client.post(`/api/v1/nodes/${nodeId}/gpio/${pin}/set`, { toggle: true })
  },

  async read(nodeId: string, pin: number): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/gpio/${pin}/read`)
    return resp?.data ?? { request_id: 0 }
  },
}

// === PWM ===

export interface PWMConfig {
  id?: number
  node_id: string
  hardware_id: string
  channel: number
  pin: number
  frequency: number    // Hz
  duty: number         // 0-10000 (0.00%-100.00%)
  resolution: number   // bits (4-20, default 14)
  auto_start: boolean
  label: string
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export const pwmApi = {
  async list(nodeId: string): Promise<PWMConfig[]> {
    const resp = await client.get<unknown, ApiEnvelope<PWMConfig[]>>(`/api/v1/nodes/${nodeId}/pwm`)
    if (resp.code && resp.code >= 400) throw new Error('获取 PWM 配置失败')
    return resp.data ?? []
  },

  async create(nodeId: string, data: Partial<PWMConfig>): Promise<PWMConfig> {
    const payload = { ...data }
    delete payload.channel
    const resp = await client.post<unknown, ApiEnvelope<PWMConfig>>(`/api/v1/nodes/${nodeId}/pwm`, payload)
    return resp.data
  },

  async update(nodeId: string, hardwareId: string, data: Partial<PWMConfig>): Promise<void> {
    await client.put(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}`, data)
  },

  async delete(nodeId: string, hardwareId: string): Promise<void> {
    await client.delete(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}`)
  },

  async start(nodeId: string, hardwareId: string): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/start`)
    return resp?.data ?? { request_id: 0 }
  },

  async stop(nodeId: string, hardwareId: string): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/stop`)
    return resp?.data ?? { request_id: 0 }
  },

  async setDuty(nodeId: string, hardwareId: string, duty: number): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/duty`, { duty })
    return resp?.data ?? { request_id: 0 }
  },

  async setFreq(nodeId: string, hardwareId: string, frequency: number): Promise<CommandAck> {
    const resp = await client.post<unknown, ApiEnvelope<CommandAck>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/freq`, { frequency })
    return resp?.data ?? { request_id: 0 }
  },

  async getState(nodeId: string, hardwareId: string): Promise<{ running: boolean; duty: number; frequency: number; request_id: number }> {
    const resp = await client.get<unknown, ApiEnvelope<{ running: boolean; duty: number; frequency: number; request_id: number }>>(`/api/v1/nodes/${nodeId}/pwm/${hardwareId}/state`)
    return resp.data
  },
}
