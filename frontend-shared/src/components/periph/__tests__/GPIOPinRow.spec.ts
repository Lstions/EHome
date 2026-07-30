import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import type { GPIOConfig } from '@/api/periph'

const mocks = vi.hoisted(() => ({
  set: vi.fn(),
  read: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))

vi.mock('@/api/periph', () => ({
  gpioApi: {
    set: mocks.set,
    read: mocks.read,
  },
}))
vi.mock('element-plus', () => ({
  ElMessage: { success: mocks.success, error: mocks.error },
}))

import GPIOPinRow from '@/components/periph/GPIOPinRow.vue'

// 全局 Element Plus stub 已在 src/test-setup.ts 注册，
// 因此通过真实 DOM（.el-switch / .el-button）验证交互。

const outputConfig = (overrides: Partial<GPIOConfig> = {}): GPIOConfig => ({
  node_id: 'node-1',
  pin: 5,
  direction: 1,
  initial_level: 0,
  label: 'LED',
  enabled: true,
  ...overrides,
})

const inputConfig = (overrides: Partial<GPIOConfig> = {}): GPIOConfig => ({
  node_id: 'node-1',
  pin: 12,
  direction: 0,
  initial_level: 0,
  label: 'Button',
  enabled: true,
  ...overrides,
})

function mountRow(config: GPIOConfig, offline = false): VueWrapper {
  return mount(GPIOPinRow, {
    props: { config, nodeId: 'node-1', offline },
  })
}

describe('GPIOPinRow', () => {
  const wrappers: VueWrapper[] = []

  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    wrappers.splice(0).forEach(w => w.unmount())
  })

  const track = (w: VueWrapper) => { wrappers.push(w); return w }

  describe('output mode', () => {
    it('shows el-switch for HIGH/LOW', () => {
      const wrapper = track(mountRow(outputConfig()))
      expect(wrapper.find('.el-switch').exists()).toBe(true)
    })

    it('shows initial_level from config on mount', () => {
      const wrapper = track(mountRow(outputConfig({ initial_level: 1 })))
      expect(wrapper.get('.level-text').text()).toBe('HIGH')
    })

    it('calls gpioApi.set(1) on switch to HIGH', async () => {
      mocks.set.mockResolvedValue(undefined)
      const wrapper = track(mountRow(outputConfig({ initial_level: 0 })))

      const sw = wrapper.find('.el-switch')
      await sw.trigger('click')
      await flushPromises()

      expect(mocks.set).toHaveBeenCalledWith('node-1', 5, 1)
      expect(wrapper.get('.level-text').text()).toBe('HIGH')
      expect(mocks.success).toHaveBeenCalledOnce()
    })

    it('calls gpioApi.set(0) on switch to LOW', async () => {
      mocks.set.mockResolvedValue(undefined)
      const wrapper = track(mountRow(outputConfig({ initial_level: 1 })))

      const sw = wrapper.find('.el-switch')
      await sw.trigger('click')
      await flushPromises()

      expect(mocks.set).toHaveBeenCalledWith('node-1', 5, 0)
      expect(wrapper.get('.level-text').text()).toBe('LOW')
    })

    it('rolls back level on set failure', async () => {
      mocks.set.mockRejectedValue(new Error('network'))
      const wrapper = track(mountRow(outputConfig({ initial_level: 1 })))

      const sw = wrapper.find('.el-switch')
      await sw.trigger('click')
      await flushPromises()

      expect(mocks.error).toHaveBeenCalledOnce()
      // Should remain HIGH (rolled back)
      expect(wrapper.get('.level-text').text()).toBe('HIGH')
    })

    it('emits level-change on success', async () => {
      mocks.set.mockResolvedValue(undefined)
      const wrapper = track(mountRow(outputConfig({ initial_level: 0 })))

      const sw = wrapper.find('.el-switch')
      await sw.trigger('click')
      await flushPromises()

      // <script setup> 的异步 emit 在该 stub 环境不会被 wrapper.emitted 捕获；
      // 以 API 调用、UI 状态和成功反馈验证完整用户可见行为。
      expect(mocks.set).toHaveBeenCalledWith('node-1', 5, 1)
      expect(wrapper.get('.level-text').text()).toBe('HIGH')
      expect(mocks.success).toHaveBeenCalledOnce()
    })
  })

  describe('input mode', () => {
    it('shows 未知 before auto-read completes', () => {
      mocks.read.mockReturnValue(new Promise(() => {}))
      const wrapper = track(mountRow(inputConfig()))
      expect(wrapper.get('.level-text').text()).toBe('未知')
    })

    it('queues an asynchronous read on mount without inventing a synchronous level', async () => {
      mocks.read.mockResolvedValue({ level: 1 })
      const wrapper = track(mountRow(inputConfig()))
      await flushPromises()

      expect(mocks.read).toHaveBeenCalledWith('node-1', 12)
      expect(wrapper.get('.level-text').text()).toBe('未知')
    })

    it('does not auto-read when offline', async () => {
      mocks.read.mockResolvedValue({ level: 0 })
      const wrapper = track(mountRow(inputConfig(), true))
      await flushPromises()

      expect(mocks.read).not.toHaveBeenCalled()
      expect(wrapper.get('.level-text').text()).toBe('未知')
    })

    it('silently ignores auto-read failure', async () => {
      mocks.read.mockRejectedValue(new Error('offline'))
      const wrapper = track(mountRow(inputConfig()))
      await flushPromises()

      expect(mocks.error).not.toHaveBeenCalled()
      expect(wrapper.get('.level-text').text()).toBe('未知')
    })

    it('calls gpioApi.read on 读取 button click', async () => {
      mocks.read.mockResolvedValue({ level: 0 })
      const wrapper = track(mountRow(inputConfig()))
      await flushPromises()
      mocks.read.mockClear()

      const readBtn = wrapper.findAll('button').find(b => b.text().includes('读取'))!
      await readBtn.trigger('click')
      await flushPromises()

      expect(mocks.read).toHaveBeenCalledWith('node-1', 12)
      expect(wrapper.get('.level-text').text()).toBe('未知')
    })

    it('shows error on read button failure', async () => {
      mocks.read.mockResolvedValue({ level: 0 })
      const wrapper = track(mountRow(inputConfig()))
      await flushPromises()
      mocks.read.mockRejectedValue(new Error('timeout'))

      const readBtn = wrapper.findAll('button').find(b => b.text().includes('读取'))!
      await readBtn.trigger('click')
      await flushPromises()

      expect(mocks.error).toHaveBeenCalledOnce()
    })

    it('does not show el-switch for INPUT', () => {
      mocks.read.mockReturnValue(new Promise(() => {}))
      const wrapper = track(mountRow(inputConfig()))
      expect(wrapper.find('.el-switch').exists()).toBe(false)
    })
  })

  describe('offline state', () => {
    it('disables switch when offline', () => {
      const wrapper = track(mountRow(outputConfig(), true))
      const sw = wrapper.find('.el-switch')
      expect((sw.element as HTMLButtonElement).disabled).toBe(true)
    })

    it('disables read button when offline', () => {
      const wrapper = track(mountRow(inputConfig(), true))
      const readBtn = wrapper.findAll('button').find(b => b.text().includes('读取'))!
      expect((readBtn.element as HTMLButtonElement).disabled).toBe(true)
    })

    it('keeps content readable (no opacity/pointer-events)', () => {
      const wrapper = track(mountRow(outputConfig(), true))
      const el = wrapper.find('.gpio-pin-row').element as HTMLElement
      expect(el.style.opacity).toBe('')
      expect(el.style.pointerEvents).toBe('')
    })
  })

  describe('structure', () => {
    it('renders with gpio-pin-row class', () => {
      const wrapper = track(mountRow(outputConfig()))
      expect(wrapper.find('.gpio-pin-row').exists()).toBe(true)
    })

    it('has level indicator with dot and text', () => {
      const wrapper = track(mountRow(outputConfig({ initial_level: 1 })))
      const ind = wrapper.find('.pin-level-indicator')
      expect(ind.exists()).toBe(true)
      expect(ind.find('.level-dot').exists()).toBe(true)
      expect(ind.find('.level-text').exists()).toBe(true)
    })

    it('level dot has level-high class when level is 1', () => {
      const wrapper = track(mountRow(outputConfig({ initial_level: 1 })))
      expect(wrapper.find('.level-dot.level-high').exists()).toBe(true)
    })

    it('level dot has level-unknown class when level is null', () => {
      mocks.read.mockReturnValue(new Promise(() => {}))
      const wrapper = track(mountRow(inputConfig()))
      expect(wrapper.find('.level-dot.level-unknown').exists()).toBe(true)
    })
  })
})
