import { describe, expect, it } from 'vitest'
import dashboardSource from '@/views/dashboard/Dashboard.vue?raw'
import configSource from '@/views/config/DeviceConfigList.vue?raw'
import edgeDeviceSource from '@/views/edge-device/EdgeDeviceList.vue?raw'
import nodeSource from '@/views/node/NodeList.vue?raw'

const mobileStatCardPages = [
  ['Dashboard', dashboardSource, '.dashboard-stats', false],
  ['DeviceConfigList', configSource, '.stats-row', false],
  ['EdgeDeviceList', edgeDeviceSource, '.stats-row', false],
  ['NodeList', nodeSource, '.stats-row', false],
] as const

describe('mobile statistic card layout contract', () => {
  it.each(mobileStatCardPages)('%s keeps four compact, readable cards at <=768px', (_name, source, gridSelector) => {
    expect(source).toContain('@media (max-width: 768px)')
    const escapedGridSelector = gridSelector.replace('.', '\\.')
    expect(source).toMatch(new RegExp(`${escapedGridSelector}\\s*\\{[\\s\\S]*?grid-template-columns:\\s*repeat\\(4, minmax\\(0, 1fr\\)\\);[\\s\\S]*?gap:\\s*8px;`))
    expect(source).toContain('font-size: 10px')
    expect(source).toContain('line-height: 1.3')
    expect(source).toContain('max-height: 2.6em')
    expect(source).toContain('overflow: hidden')
    expect(source).toContain('word-break: keep-all')
    expect(source).toContain('overflow-wrap: break-word')
  })

  it('uses concise mobile-only labels for the two long edge-device metrics', () => {
    expect(edgeDeviceSource).toContain('mobile-label="边缘设备"')
    expect(edgeDeviceSource).toContain('mobile-label="离线/异常"')
  })

  it.each([
    ['DeviceConfigList', configSource],
    ['EdgeDeviceList', edgeDeviceSource],
    ['NodeList', nodeSource],
  ] as const)('%s retains its two-column intermediate breakpoint', (_name, source) => {
    expect(source).toContain('@media (max-width: 1200px)')
    expect(source).toContain('grid-template-columns: repeat(2, 1fr)')
  })
})
