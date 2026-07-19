import { describe, expect, it } from 'vitest'
import source from '../DeviceHeader.vue?raw'

describe('DeviceHeader cache consistency', () => {
  it('keeps editable metadata cache-safe without exposing the retired address write', () => {
    expect(source).toContain('edgeDeviceStore.invalidateLists()')
    expect(source).toContain('edgeDeviceStore.invalidateDetail(deviceId)')
    expect(source).toContain('assertSessionGeneration(generation)')
    expect(source).toContain('props.device?.id !== deviceId')
    expect(source).not.toContain('changeAddress')
    expect(source).not.toContain('修改地址')
    expect(source).toContain('onUnmounted(() =>')
    expect(source).toContain('operationGeneration++')
  })

  it('deletes through the store so list, detail and in-flight caches are invalidated', () => {
    expect(source).toContain('edgeDeviceStore.deleteDevice(deviceId)')
    expect(source).not.toContain('edgeDeviceApi.delete(props.device.id)')
  })
})
