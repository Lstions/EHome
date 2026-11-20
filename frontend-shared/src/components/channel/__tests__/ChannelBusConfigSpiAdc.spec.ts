import { describe, expect, it } from 'vitest'
import { buildChannelSubmitBusPayload } from '../channelBusConfig'
import type { ChannelFormState, HardwareResource } from '../channelFormModel'

const form = (hardware_type: ChannelFormState['hardware_type'], config: Record<string, any>): ChannelFormState => ({
  name: '', hardware_type, hardware_id: '', address: '', enabled: true, config,
})

describe('channelBusConfig SPI/ADC 编码', () => {
  it('SPI 使用 ResourceReport 的 default_*_pin 字段，不把缺失值写成 0xFF', () => {
    const hw: HardwareResource = {
      id: 'SPI2', default_cs_pin: 5, default_mosi_pin: 23,
      default_miso_pin: 19, default_sclk_pin: 18, max_freq_hz: 40000000,
    }
    const out = buildChannelSubmitBusPayload(form('spi', { spi_mode: 0, freq_hz: 1000000 }), hw, 9600)
    expect(out.busConfig).toBe('0500000F4240171312')
  })

  it('SPI 缺少引脚能力时返回空 busConfig，而不是设备必然拒绝的 0xFF', () => {
    const hw: HardwareResource = { id: 'SPI2', default_cs_pin: 5 }
    const out = buildChannelSubmitBusPayload(form('spi', { spi_mode: 0, freq_hz: 1000000 }), hw, 9600)
    expect(out.busConfig).toBe('')
  })

  it('ADC 编码 unit/channel/pin/attenuation/bit_width 五字节', () => {
    const hw: HardwareResource = { id: 'ADC1_CH1', unit: 1, channel: 1, pin: 1, max_bits: 12 }
    const out = buildChannelSubmitBusPayload(form('adc', { attenuation: 3, bit_width: 12 }), hw, 9600)
    expect(out.busConfig).toBe('010101030C')
  })
})
