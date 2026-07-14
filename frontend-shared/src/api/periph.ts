// src/api/periph.ts — GPIO + PWM 外设控制 API
import client from './client'

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
    const resp = await client.get(`/api/v1/nodes/${nodeId}/gpio`)
    const body = resp as any
    if (body.code && body.code >= 400) throw new Error('获取 GPIO 配置失败')
    return body.data || body || []
  },

  async create(nodeId: string, data: Partial<GPIOConfig>): Promise<GPIOConfig> {
    const resp = await client.post(`/api/v1/nodes/${nodeId}/gpio`, data)
    return (resp as any).data || resp
  },

  async update(nodeId: string, pin: number, data: Partial<GPIOConfig>): Promise<void> {
    await client.put(`/api/v1/nodes/${nodeId}/gpio/${pin}`, data)
  },

  async delete(nodeId: string, pin: number): Promise<void> {
    await client.delete(`/api/v1/nodes/${nodeId}/gpio/${pin}`)
  },

  async set(nodeId: string, pin: number, level: 0 | 1): Promise<void> {
    await client.post(`/api/v1/nodes/${nodeId}/gpio/${pin}/set`, { level })
  },

  async toggle(nodeId: string, pin: number): Promise<void> {
    await client.post(`/api/v1/nodes/${nodeId}/gpio/${pin}/set`, { toggle: true })
  },

  async read(nodeId: string, pin: number): Promise<{ level: number }> {
    const resp = await client.post(`/api/v1/nodes/${nodeId}/gpio/${pin}/read`)
    return (resp as any).data || resp
  },
}

// === PWM ===

export interface PWMConfig {
  id?: number
  node_id: string
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
    const resp = await client.get(`/api/v1/nodes/${nodeId}/pwm`)
    const body = resp as any
    if (body.code && body.code >= 400) throw new Error('获取 PWM 配置失败')
    return body.data || body || []
  },

  async create(nodeId: string, data: Partial<PWMConfig>): Promise<PWMConfig> {
    const resp = await client.post(`/api/v1/nodes/${nodeId}/pwm`, data)
    return (resp as any).data || resp
  },

  async update(nodeId: string, pin: number, data: Partial<PWMConfig>): Promise<void> {
    await client.put(`/api/v1/nodes/${nodeId}/pwm/${pin}`, data)
  },

  async delete(nodeId: string, pin: number): Promise<void> {
    await client.delete(`/api/v1/nodes/${nodeId}/pwm/${pin}`)
  },

  async start(nodeId: string, pin: number): Promise<void> {
    await client.post(`/api/v1/nodes/${nodeId}/pwm/${pin}/start`)
  },

  async stop(nodeId: string, pin: number): Promise<void> {
    await client.post(`/api/v1/nodes/${nodeId}/pwm/${pin}/stop`)
  },

  async setDuty(nodeId: string, pin: number, duty: number): Promise<void> {
    await client.post(`/api/v1/nodes/${nodeId}/pwm/${pin}/duty`, { duty })
  },

  async setFreq(nodeId: string, pin: number, frequency: number): Promise<void> {
    await client.post(`/api/v1/nodes/${nodeId}/pwm/${pin}/freq`, { frequency })
  },

  async getState(nodeId: string, pin: number): Promise<{ running: boolean; duty: number; frequency: number }> {
    const resp = await client.get(`/api/v1/nodes/${nodeId}/pwm/${pin}/state`)
    return (resp as any).data || resp
  },
}
