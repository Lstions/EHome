import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'

// ElMessage mock — must be hoisted before component imports
const mocks = vi.hoisted(() => ({
  messageError: vi.fn(),
}))

vi.mock('element-plus', () => ({
  ElMessage: { error: mocks.messageError },
}))

import { useGuardedOperation } from '@/composables/useGuardedOperation'

interface TestRow {
  busy: boolean
  feedback: string
  level: number
}

function makeRow(): TestRow {
  return { busy: false, feedback: '', level: 0 }
}

/**
 * Wrapper component that exposes useGuardedOperation so we can test
 * the composable within a component lifecycle (onUnmounted, watch).
 */
function makeHost(opts: {
  nodeId: string
  offline?: boolean
  errorPrefix?: string
}) {
  return defineComponent({
    setup() {
      const nodeIdRef = ref(opts.nodeId)
      const offlineRef = ref(opts.offline ?? false)
      const ops = useGuardedOperation({
        nodeId: nodeIdRef,
        offline: offlineRef,
        errorPrefix: opts.errorPrefix ?? '操作失败',
      })
      return { nodeIdRef, offlineRef, ...ops }
    },
    template: '<div />',
  })
}

describe('useGuardedOperation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('sets busy and feedback during operation, clears on success', async () => {
    const wrapper = mount(makeHost({ nodeId: 'node-1' }))
    const { run } = wrapper.vm as any
    const row = makeRow()
    let resolveFn: (val: number) => void
    const pending = new Promise<number>(r => { resolveFn = r })

    const promise = run(row, '正在写入…', () => pending)
    await nextTick()
    expect(row.busy).toBe(true)
    expect(row.feedback).toBe('正在写入…')

    resolveFn!(42)
    const result = await promise
    expect(result).toBe(42)
    expect(row.busy).toBe(false)
    wrapper.unmount()
  })

  it('blocks new operations when row is already busy', async () => {
    const wrapper = mount(makeHost({ nodeId: 'node-1' }))
    const { run } = wrapper.vm as any
    const row = makeRow()
    row.busy = true

    const result = await run(row, '正在写入…', async () => 1)
    expect(result).toBeUndefined()
    // busy was never set by run (entry guard blocked)
    expect(row.busy).toBe(true)
    wrapper.unmount()
  })

  it('blocks operations when offline', async () => {
    const wrapper = mount(makeHost({ nodeId: 'node-1', offline: true }))
    const { run } = wrapper.vm as any
    const row = makeRow()

    const result = await run(row, '正在写入…', async () => 1)
    expect(result).toBeUndefined()
    expect(row.busy).toBe(false) // never set
    wrapper.unmount()
  })

  it('uses errorLabel and errorFeedback overrides on failure', async () => {
    const wrapper = mount(makeHost({ nodeId: 'node-1', errorPrefix: '默认前缀' }))
    const { run } = wrapper.vm as any
    const row = makeRow()
    row.level = 5

    const result = await run(row, '正在写入…', async () => {
      throw new Error('timeout')
    }, {
      errorLabel: 'GPIO 写入失败',
      errorFeedback: '写入失败 · 重试',
      rollback: () => { row.level = 0 },
    })

    expect(result).toBeUndefined()
    expect(row.feedback).toBe('写入失败 · 重试')
    expect(row.level).toBe(0) // rollback was called
    expect(mocks.messageError).toHaveBeenCalledWith('GPIO 写入失败: timeout')
    expect(row.busy).toBe(false)
    wrapper.unmount()
  })

  it('falls back to errorPrefix and default feedback when no overrides', async () => {
    const wrapper = mount(makeHost({ nodeId: 'node-1', errorPrefix: 'GPIO 操作失败' }))
    const { run } = wrapper.vm as any
    const row = makeRow()

    await run(row, '正在读取…', async () => {
      throw new Error('disconnected')
    })

    expect(row.feedback).toBe('操作失败 · 重试')
    expect(mocks.messageError).toHaveBeenCalledWith('GPIO 操作失败: disconnected')
    wrapper.unmount()
  })

  it('uses default error message for non-Error throw', async () => {
    const wrapper = mount(makeHost({ nodeId: 'node-1', errorPrefix: 'PWM 操作失败' }))
    const { run } = wrapper.vm as any
    const row = makeRow()

    await run(row, '正在启动…', async () => {
      throw 'string error'
    })

    expect(mocks.messageError).toHaveBeenCalledWith('PWM 操作失败: 未知错误')
    wrapper.unmount()
  })

  it('skips entry guard when skipEntryGuard is true', async () => {
    const wrapper = mount(makeHost({ nodeId: 'node-1' }))
    const { run } = wrapper.vm as any
    const row = makeRow()
    row.busy = true // would normally block

    const result = await run(row, '待应用', async () => 99, { skipEntryGuard: true })
    expect(result).toBe(99)
    wrapper.unmount()
  })

  it('invalidates in-flight operations after invalidate() is called', async () => {
    const wrapper = mount(makeHost({ nodeId: 'node-1' }))
    const { run, invalidate } = wrapper.vm as any
    const row = makeRow()

    let resolveFn: (val: number) => void
    const pending = new Promise<number>(r => { resolveFn = r })
    const promise = run(row, '正在写入…', () => pending)

    invalidate()
    resolveFn!(42)
    const result = await promise
    expect(result).toBeUndefined()
    // busy should still be cleared (isStale returns true → finally skips)
    wrapper.unmount()
  })

  it('invalidates operations on unmount', async () => {
    const wrapper = mount(makeHost({ nodeId: 'node-1' }))
    const { run } = wrapper.vm as any
    const row = makeRow()

    let resolveFn: (val: number) => void
    const pending = new Promise<number>(r => { resolveFn = r })
    const promise = run(row, '正在写入…', () => pending)

    wrapper.unmount()
    resolveFn!(42)
    const result = await promise
    expect(result).toBeUndefined()
  })
})
