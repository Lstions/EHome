import { describe, expect, it } from 'vitest'
import source from '../ChannelPanel.vue?raw'

describe('ChannelPanel route-scoped data', () => {
  it('reloads and rejects stale responses when collector props change', () => {
    expect(source).toContain('watch(() => [props.collectorId, props.nodeDeviceId]')
    expect(source).toContain('panelGeneration++')
    expect(source).toContain('generation !== panelGeneration')
    expect(source).toContain('sequence !== busesRequestSequence')
    expect(source).toContain('sequence !== channelsRequestSequence')
    expect(source).toContain('props.nodeDeviceId !== deviceId')
    expect(source).toContain('gpioConfigs.value = []')
    expect(source).toContain('pwmConfigs.value = []')
    expect(source).toContain('void refreshPeriph()')
    expect(source).toContain('channelManagerVisible.value = false')
    expect(source).toContain('reconfigureDialogVisible.value = false')
    expect(source).toContain('channelStore.deleteChannel(channelId)')
    expect(source).toContain('assertSessionGeneration(sessionGeneration)')
    expect(source).toContain('scanningHwId.value = null')
    expect(source).toContain('reconfigureLoading.value = false')
    expect(source).toContain('saving.value = false')
    expect(source).toContain('props.collectorId !== collectorId')
  })

  it('keeps reported GPIO and PWM identities independent and submits PWM routes by hardware id', () => {
    expect(source).toContain("for (const type of ['adc', 'i2c', 'spi', 'uart', 'gpio', 'pwm'])")
    expect(source).toContain("import GPIOResourceList from '@/components/periph/GPIOResourceList.vue'")
    expect(source).toContain("import PWMResourceList from '@/components/periph/PWMResourceList.vue'")
    expect(source).not.toContain("import PinResourceList from '@/components/periph/PinResourceList.vue'")
    expect(source).toContain('const openPwmDialogFromRow = (hardwareId: string)')
    expect(source).toContain('hardware_id: pwmForm.hardware_id')
    expect(source).not.toContain('channel: selectedPwmResource.value?.channel')
    expect(source).toContain('await pwmApi.delete(nodeId, hardwareId)')
    expect(source).toContain('v-model="pwmForm.pin"')
    expect(source).toContain('v-for="pin in availablePwmRoutePins"')
  })
})