import { describe, expect, it } from 'vitest'
import layoutSource from '../MainLayout.vue?raw'

describe('MainLayout route navigation UX', () => {
  it('does not remove the current route before the next route is ready', () => {
    expect(layoutSource).not.toContain('mode="out-in"')
  })

  it('preloads the primary list routes after the authenticated layout mounts', () => {
    expect(layoutSource).toContain('preloadPrimaryRoutes')
  })

  it('warms the primary list data caches before the first menu click', () => {
    expect(layoutSource).toContain('warmPrimaryListData')
    expect(layoutSource).toContain('Promise.allSettled')
  })

  it('uses the unified user logout path for session cache clearing', () => {
    expect(layoutSource).toContain('await userStore.logout()')
  })
})