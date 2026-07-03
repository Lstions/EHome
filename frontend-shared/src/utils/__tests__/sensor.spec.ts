import { describe, it, expect } from 'vitest'
import { sensorNameMap, sensorUnitMap } from '@/utils/sensor'

describe('sensor.ts BMS mappings', () => {
  const bmsKeys = [
    'total_voltage', 'rsoc', 'remaining_capacity', 'protection_status', 'fet_status',
    'temperature_1', 'temperature_2', 'temperature_3', 'cell_voltage_max', 'cell_voltage_min',
  ]

  it('all BMS keys exist in sensorNameMap', () => {
    for (const key of bmsKeys) {
      expect(sensorNameMap).toHaveProperty(key)
      expect(typeof sensorNameMap[key]).toBe('string')
      expect(sensorNameMap[key].length).toBeGreaterThan(0)
    }
  })

  it('all BMS keys exist in sensorUnitMap', () => {
    for (const key of bmsKeys) {
      expect(sensorUnitMap).toHaveProperty(key)
      expect(typeof sensorUnitMap[key]).toBe('string')
    }
  })

  it('total_voltage has correct name and unit', () => {
    expect(sensorNameMap.total_voltage).toBe('总电压')
    expect(sensorUnitMap.total_voltage).toBe('V')
  })

  it('rsoc has correct name and unit', () => {
    expect(sensorNameMap.rsoc).toBe('SOC')
    expect(sensorUnitMap.rsoc).toBe('%')
  })

  it('remaining_capacity has correct name and unit', () => {
    expect(sensorNameMap.remaining_capacity).toBe('剩余容量')
    expect(sensorUnitMap.remaining_capacity).toBe('Ah')
  })

  it('fet_status has correct name and empty unit', () => {
    expect(sensorNameMap.fet_status).toBe('MOS状态')
    expect(sensorUnitMap.fet_status).toBe('')
  })

  it('cell_voltage_max/min have correct names and units', () => {
    expect(sensorNameMap.cell_voltage_max).toBe('最高单体电压')
    expect(sensorUnitMap.cell_voltage_max).toBe('V')
    expect(sensorNameMap.cell_voltage_min).toBe('最低单体电压')
    expect(sensorUnitMap.cell_voltage_min).toBe('V')
  })

  it('temperature_1/2/3 have correct names and units', () => {
    expect(sensorNameMap.temperature_1).toBe('温度1')
    expect(sensorUnitMap.temperature_1).toBe('°C')
    expect(sensorNameMap.temperature_2).toBe('温度2')
    expect(sensorUnitMap.temperature_2).toBe('°C')
    expect(sensorNameMap.temperature_3).toBe('温度3')
    expect(sensorUnitMap.temperature_3).toBe('°C')
  })

  it('protection_status has correct name', () => {
    expect(sensorNameMap.protection_status).toBe('保护状态')
  })

  it('current (shared with non-BMS) still exists with correct values', () => {
    expect(sensorNameMap.current).toBe('电流')
    expect(sensorUnitMap.current).toBe('A')
  })
})
