import { describe, expect, it } from 'vitest'
import source from '../ChannelPanel.vue?raw'

describe('ChannelPanel route-scoped data', () => {
  it('reloads and rejects stale responses when collector props change', () => {
    expect(source).toContain('watch(() => [props.collectorId, props.nodeDeviceId]')
    expect(source).toContain('panelGeneration++')
    expect(source).toContain('generation !== panelGeneration')
    expect(source).toContain('sequence !== busesRequestSequence')
    expect(source).toContain('sequence !== channelsRequestSequence')
    expect(source).toContain('channelManagerVisible.value = false')
    expect(source).toContain('reconfigureDialogVisible.value = false')
    expect(source).toContain('channelStore.deleteChannel(channelId)')
    expect(source).toContain('assertSessionGeneration(sessionGeneration)')
    expect(source).toContain('scanningHwId.value = null')
    expect(source).toContain('reconfigureLoading.value = false')
    expect(source).toContain('saving.value = false')
    expect(source).toContain('props.collectorId !== collectorId')
  })
})