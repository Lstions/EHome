import { describe, expect, it } from 'vitest'
import source from '../DeviceHeader.vue?raw'

describe('DeviceHeader cache consistency', () => {
  it('invalidates parameterized lists after detail-page writes', () => {
    expect(source.match(/edgeDeviceStore\.invalidateLists\(\)/g)).toHaveLength(2)
    expect(source.match(/edgeDeviceStore\.invalidateDetail\(deviceId\)/g)).toHaveLength(2)
    expect(source.match(/assertSessionGeneration\(generation\)/g)).toHaveLength(2)
    expect(source.match(/props\.device\?\.id !== deviceId/g)).toHaveLength(6)
    expect(source).toContain('onUnmounted(() =>')
    expect(source).toContain('operationGeneration++')
  })

  it('deletes through the store so list, detail and in-flight caches are invalidated', () => {
    expect(source).toContain('edgeDeviceStore.deleteDevice(deviceId)')
    expect(source).not.toContain('edgeDeviceApi.delete(props.device.id)')
  })
})