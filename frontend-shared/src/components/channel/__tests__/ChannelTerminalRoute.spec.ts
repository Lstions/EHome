import { describe, expect, it } from 'vitest'
import source from '../ChannelTerminal.vue?raw'

describe('ChannelTerminal collector isolation', () => {
  it('invalidates local channel requests and terminal state when collector changes', () => {
    expect(source).toContain('channelRequestGeneration++')
    expect(source).toContain('props.collectorId !== collectorId')
    expect(source).toContain('props.nodeDeviceId !== nodeDeviceId')
    expect(source).toContain('watch(() => [props.collectorId, props.nodeDeviceId]')
    expect(source).toContain('selectedChannelId.value = undefined')
    expect(source).toContain('localChannels.value = []')
    expect(source).toContain('clearLog()')
    expect(source).toContain('generation !== channelRequestGeneration')
    expect(source).toContain('sending.value = false')
  })
})
