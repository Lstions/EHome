import { beforeEach, describe, expect, it, vi } from 'vitest'
import { assertSessionGeneration, clearSessionCaches, getSessionGeneration, registerSessionCacheClearer } from '../sessionCache'

describe('session cache registry', () => {
  beforeEach(() => {
    clearSessionCaches()
  })

  it('clears every registered user-scoped cache', () => {
    const clearNode = vi.fn()
    const clearEdge = vi.fn()
    const unregisterNode = registerSessionCacheClearer(clearNode)
    const unregisterEdge = registerSessionCacheClearer(clearEdge)

    clearSessionCaches()

    expect(clearNode).toHaveBeenCalledOnce()
    expect(clearEdge).toHaveBeenCalledOnce()
    unregisterNode()
    unregisterEdge()
  })

  it('does not call an unregistered cache clearer', () => {
    const clear = vi.fn()
    const unregister = registerSessionCacheClearer(clear)
    unregister()
    clearSessionCaches()
    expect(clear).not.toHaveBeenCalled()
  })

  it('invalidates captured session generations when caches are cleared', () => {
    const generation = getSessionGeneration()
    clearSessionCaches()
    expect(() => assertSessionGeneration(generation)).toThrow('会话已变更')
  })
})