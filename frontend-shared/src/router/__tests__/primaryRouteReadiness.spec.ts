import { describe, expect, it } from 'vitest'
import mainSource from '@/main.ts?raw'

describe('primary route readiness', () => {
  it('does not block navigation on list API readiness', () => {
    expect(mainSource).not.toContain('router.beforeResolve')
    expect(mainSource).not.toContain('preparePrimaryRoute')
  })
})