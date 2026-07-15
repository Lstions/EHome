import { describe, expect, it } from 'vitest'
import source from '../OTAForm.vue?raw'

describe('OTAForm route isolation', () => {
  it('binds OTA start and polling to the originating collector generation', () => {
    expect(source).toContain('const collectorId = props.collectorId')
    expect(source).toContain('const generation = ++otaGeneration')
    expect(source).toContain('getOTAProgress(collectorId, recordId)')
    expect(source).toContain('generation !== otaGeneration')
    expect(source).toContain('watch(() => props.collectorId')
    expect(source).toContain('onUnmounted(() =>')
    expect(source).toContain(':before-close="handleDialogClose"')
    expect(source).toContain('otaGeneration++')
    expect(source).toContain('closeGeneration === otaGeneration')
  })
})
