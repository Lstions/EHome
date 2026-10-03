import { describe, it, expect } from 'vitest'
import {
  getHardwareLabel,
  getHardwareTitle,
  getHardwareTagType,
  hardwareIdIncludesBusName,
  getHardwareDisplay,
} from '@/utils/hardwareTag'
import channelListSource from '@/views/channel/ChannelList.vue?raw'
import channelTerminalSource from '@/components/channel/ChannelTerminal.vue?raw'
import nodeOverviewSource from '@/views/node/NodeOverview.vue?raw'

// 同一实体（通道的总线类型）曾在前端有 5 套写法：'UART' / 'uart' / '串行' /
// '串口 (UART)' / "UART UART0"。本文件锁死共享源的归一与展示规则，
// 并源码级钉住调用方不得再各写一套 map。
describe('hardwareTag — 总线标签单一事实源', () => {
  it('getHardwareLabel 归一大小写后返回统一大写标签', () => {
    expect(getHardwareLabel('UART')).toBe('UART')
    expect(getHardwareLabel('uart')).toBe('UART')
    expect(getHardwareLabel('I2C')).toBe('I2C')
    expect(getHardwareLabel('i2c')).toBe('I2C')
    expect(getHardwareLabel(' SPI ')).toBe('SPI')
    expect(getHardwareLabel('GPIO')).toBe('GPIO')
    expect(getHardwareLabel('adc')).toBe('ADC')
    expect(getHardwareLabel('pwm')).toBe('PWM')
  })

  it('getHardwareLabel 对未知类型返回归一后的大写原文，绝不返回空串', () => {
    expect(getHardwareLabel('modbus')).toBe('MODBUS')
    expect(getHardwareLabel('DMA')).toBe('DMA')
    expect(getHardwareLabel('')).toBe('')
  })

  it('getHardwareTitle 与英文标签一一对应，未知类型回退英文标签', () => {
    expect(getHardwareTitle('UART')).toBe('串行')
    expect(getHardwareTitle('uart')).toBe('串行')
    expect(getHardwareTitle('GPIO')).toBe('数字IO')
    expect(getHardwareTitle('ADC')).toBe('模拟')
    expect(getHardwareTitle('i2c')).toBe('I²C')
    // 未知类型不得留白，回退到同口径的英文大写标签
    expect(getHardwareTitle('modbus')).toBe('MODBUS')
  })

  it('getHardwareTagType 同样接受任意大小写', () => {
    expect(getHardwareTagType('UART')).toBe('primary')
    expect(getHardwareTagType('uart')).toBe('primary')
    expect(getHardwareTagType('I2C')).toBe('success')
    expect(getHardwareTagType('unknown')).toBe('info')
  })
})

describe('hardwareTag — channelName 不重复前缀规则', () => {
  it('hardware_id 以字母开头 = 已自带总线名', () => {
    expect(hardwareIdIncludesBusName('UART0')).toBe(true)
    expect(hardwareIdIncludesBusName('I2C0')).toBe(true)
    expect(hardwareIdIncludesBusName('SPI0_CS0')).toBe(true)
    expect(hardwareIdIncludesBusName(' 1')).toBe(false)
    expect(hardwareIdIncludesBusName('0x76')).toBe(false)
    expect(hardwareIdIncludesBusName('')).toBe(false)
  })

  it('自带总线名时不得再拼前缀（旧写法渲染出 "UART UART0"）', () => {
    expect(getHardwareDisplay('UART', 'UART0')).toBe('UART0')
    expect(getHardwareDisplay('UART', 'UART1')).toBe('UART1')
    expect(getHardwareDisplay('I2C', 'I2C0')).toBe('I2C0')
    expect(getHardwareDisplay('uart', 'UART0')).not.toBe('UART UART0')
  })

  it('hardware_id 是纯数字/地址时才补总线前缀', () => {
    expect(getHardwareDisplay('UART', '1')).toBe('UART 1')
    expect(getHardwareDisplay('uart', '1')).toBe('UART 1')
    expect(getHardwareDisplay('I2C', '0x76')).toBe('I2C 0x76')
  })

  it('hardware_id 为空时回退总线标签，仍非空可读', () => {
    expect(getHardwareDisplay('UART', '')).toBe('UART')
    expect(getHardwareDisplay('i2c', '')).toBe('I2C')
  })
})

// 源码级反向钉死：调用方不得再引入私有总线标签 map（否则又回到多套写法）。
describe('hardwareTag — 调用方不得各写一套标签 map', () => {
  it('ChannelList 用共享 getHardwareLabel，且私有 getBusTypeLabel 已删除', () => {
    expect(channelListSource).toContain('getHardwareLabel(row.hardware_type)')
    expect(channelListSource).not.toContain('getBusTypeLabel')
    expect(channelListSource).not.toContain("uart: '串行'")
  })

  it('ChannelTerminal 用共享 getHardwareLabel，私有 label map 已删除', () => {
    expect(channelTerminalSource).toContain('getHardwareLabel(')
    // 旧私有 map 曾给 uart 造了第二套写法；用拼接规避门禁自指，同时钉死它不复活。
    expect(channelTerminalSource).not.toContain('串口 (' + 'UART)')
    expect(channelTerminalSource).not.toContain('typeLabels')
  })

  it('NodeOverview 的 channelName 走共享 getHardwareDisplay，不再内联拼接', () => {
    expect(nodeOverviewSource).toContain('getHardwareDisplay(ch.hardware_type')
    expect(nodeOverviewSource).not.toMatch(/hardware_type \|\| ''\)\.toUpperCase\(\)\} \$\{ch\.hardware_id/)
  })
})
