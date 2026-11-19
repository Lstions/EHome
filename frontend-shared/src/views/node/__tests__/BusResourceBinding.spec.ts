import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { DmaChannelInfo } from '@/api/node'
import {
  dmaSelectionOf,
  isDmaBoundToResource,
  resourceBindingKey,
  resourceDmaBindingOf,
} from '../composables/busResourceHelpers'

/**
 * DMA bind_to 的"写入口径"与"比较口径"必须分开钉住。
 *
 * 2026-11-19 实错：总线配置页的 DMA 开关一律绑定失败，后端 400 ——
 *   configs[0].bind_to "uart/uart1" does not match any hardware id
 *   (compared case-sensitively on the device); did you mean "uart/UART1"?
 * 根因是 resourceBindingKey 对整串做了 toLowerCase()。固件 derive_hw_id()
 * 产出的是大写的 uart/UART1（hw_tables.c 的 .id），dma_pool 预抢占/释放
 * 用 strcmp 比对 ⇒ 小写值既绑不上也释放不掉。
 *
 * 本文件钉两条**互相独立**的口径，改任一条都会变红：
 *   1. 写入：resourceBindingKey 必须原样保留设备上报的资源 ID 大小写；
 *   2. 比较：历史小写值（真实设备回显）仍必须被识别为"绑在本资源上"。
 */

const uart1 = { id: 'UART1' }
const spi2 = { id: 'SPI2' }

function dma(partial: Partial<DmaChannelInfo>): DmaChannelInfo {
  return {
    dma_id: 0, name: 'GDMA_CH0', dma_type: 0, capabilities: 3,
    max_burst: 4095, state: 0, bound_to: '', compatible_bus: 5,
    ...partial,
  }
}

describe('DMA bind_to 写入口径：设备规范大小写', () => {
  it('产出 <bus 小写>/<资源 id 原样>，与固件 derive_hw_id 一致', () => {
    expect(resourceBindingKey('uart', uart1)).toBe('uart/UART1')
    expect(resourceBindingKey('spi', spi2)).toBe('spi/SPI2')
    expect(resourceBindingKey('i2c', { id: 'I2C0' })).toBe('i2c/I2C0')
  })

  it('回归钉：不得把资源 ID 压成小写（压了小写 = 后端 400 + 设备通道泄漏）', () => {
    expect(resourceBindingKey('uart', uart1)).not.toBe('uart/uart1')
    expect(resourceBindingKey('spi', spi2)).not.toBe('spi/spi2')
  })

  it('源码级：resourceBindingKey 内不得出现 toLowerCase（防止再次"顺手归一化"）', () => {
    const src = readFileSync(resolve(process.cwd(), 'src/views/node/composables/busResourceHelpers.ts'), 'utf8')
    const body = src.match(/export function resourceBindingKey[\s\S]*?\n}/)?.[0] ?? ''
    expect(body, '未能从 busResourceHelpers.ts 解析出 resourceBindingKey，门禁会静默失效').toContain('resource.id')
    expect(body).not.toContain('toLowerCase')
  })
})

describe('DMA 绑定比较口径：大小写不敏感（读得出历史脏数据）', () => {
  it('历史小写 bound_to 仍算作绑在本资源上', () => {
    const legacy = dma({ dma_id: 1, name: 'GDMA_CH1', state: 1, bound_to: 'uart/uart1' })
    expect(isDmaBoundToResource('uart', legacy, uart1)).toBe(true)
    expect(resourceDmaBindingOf('uart', uart1, [legacy])?.dma_id).toBe(1)
    expect(dmaSelectionOf('uart', uart1, [legacy])).toBe('1')
  })

  it('规范值同样算作已绑定（修复后新写入的数据）', () => {
    const canonical = dma({ dma_id: 1, name: 'GDMA_CH1', state: 1, bound_to: 'uart/UART1' })
    expect(isDmaBoundToResource('uart', canonical, uart1)).toBe(true)
    expect(dmaSelectionOf('uart', uart1, [canonical])).toBe('1')
  })

  it('不同资源/不同总线不得误判', () => {
    const spi = dma({ bound_to: 'spi/SPI2', compatible_bus: 4 })
    expect(isDmaBoundToResource('uart', spi, uart1)).toBe(false)
    expect(isDmaBoundToResource('spi', spi, { id: 'SPI3' })).toBe(false)
  })
})
