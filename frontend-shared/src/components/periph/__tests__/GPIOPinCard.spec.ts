import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent } from 'vue'
import type { GPIOConfig } from '@/api/periph'

const mocks = vi.hoisted(() => ({
  set: vi.fn(),
  toggle: vi.fn(),
  read: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}))

vi.mock('@/api/periph', () => ({
  gpioApi: {
    set: mocks.set,
    toggle: mocks.toggle,
    read: mocks.read,
  },
}))
vi.mock('element-plus', () => ({
  ElMessage: { success: mocks.success, error: mocks.error },
}))

import GPIOPinCard from '@/components/periph/GPIOPinCard.vue'

const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: ['loading', 'disabled', 'type', 'size'],
  emits: ['click'],
  computed: {
    isDisabled(): boolean {
      return Boolean(this.disabled)
    },
  },
  template: `<button v-bind="$attrs" :disabled="isDisabled" :data-loading="String(Boolean(loading))" @click="$emit('click')"><slot /></button>`,
})

const TagStub = defineComponent({
  inheritAttrs: false,
  props: ['type', 'size'],
  template: `<span v-bind="$attrs"><slot /></span>`,
})

const stubs = {
  'el-button': ButtonStub,
  'el-tag': TagStub,
}

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

function mountCard(config: GPIOConfig, offline = false): VueWrapper {
  return mount(GPIOPinCard, {
    props: { config, nodeId: 'node-1', offline },
    global: { stubs },
  })
}

describe('GPIOPinCard', () => {
  const wrappers: VueWrapper[] = []

  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    wrappers.splice(0).forEach(w => w.unmount())
  })

  const track = (wrapper: VueWrapper) => {
    wrappers.push(wrapper)
    return wrapper
  }

  describe('output mode', () => {
    it('calls gpioApi.set(1) on ON and updates level to HIGH', async () => {
      mocks.set.mockResolvedValue(undefined)
      const wrapper = track(mountCard(outputConfig()))

      const buttons = wrapper.findAll('button')
      const onBtn = buttons.find(b => b.text().includes('ON'))!
      await onBtn.trigger('click')
      await flushPromises()

      expect(mocks.set).toHaveBeenCalledWith('node-1', 5, 1)
      expect(wrapper.get('.level-text').text()).toBe('HIGH')
      expect(wrapper.find('.level-dot.level-high').exists()).toBe(true)
      expect(mocks.success).toHaveBeenCalledOnce()
    })

    it('calls gpioApi.set(0) on OFF and updates level to LOW', async () => {
      mocks.set.mockResolvedValue(undefined)
      const wrapper = track(mountCard(outputConfig({ initial_level: 1 })))

      const buttons = wrapper.findAll('button')
      const offBtn = buttons.find(b => b.text().includes('OFF'))!
      await offBtn.trigger('click')
      await flushPromises()

      expect(mocks.set).toHaveBeenCalledWith('node-1', 5, 0)
      expect(wrapper.get('.level-text').text()).toBe('LOW')
      expect(wrapper.find('.level-dot.level-low').exists()).toBe(true)
      expect(mocks.success).toHaveBeenCalledOnce()
    })

    it('calls gpioApi.toggle and flips level from LOW to HIGH', async () => {
      mocks.toggle.mockResolvedValue(undefined)
      const wrapper = track(mountCard(outputConfig({ initial_level: 0 })))

      const buttons = wrapper.findAll('button')
      const toggleBtn = buttons.find(b => b.text().includes('TOGGLE'))!
      await toggleBtn.trigger('click')
      await flushPromises()

      expect(mocks.toggle).toHaveBeenCalledWith('node-1', 5)
      expect(wrapper.get('.level-text').text()).toBe('HIGH')
      expect(mocks.success).toHaveBeenCalledOnce()
    })

    it('flips level from HIGH to LOW on TOGGLE', async () => {
      mocks.toggle.mockResolvedValue(undefined)
      const wrapper = track(mountCard(outputConfig({ initial_level: 1 })))

      const buttons = wrapper.findAll('button')
      const toggleBtn = buttons.find(b => b.text().includes('TOGGLE'))!
      await toggleBtn.trigger('click')
      await flushPromises()

      expect(mocks.toggle).toHaveBeenCalledWith('node-1', 5)
      expect(wrapper.get('.level-text').text()).toBe('LOW')
    })

    it('shows error message when set API fails', async () => {
      mocks.set.mockRejectedValue(new Error('network down'))
      const wrapper = track(mountCard(outputConfig()))

      const buttons = wrapper.findAll('button')
      const onBtn = buttons.find(b => b.text().includes('ON'))!
      await onBtn.trigger('click')
      await flushPromises()

      expect(mocks.error).toHaveBeenCalledOnce()
      // level should not change on failure
      expect(wrapper.get('.level-text').text()).toBe('LOW')
    })

    it('shows initial_level from config on mount', () => {
      const wrapper = track(mountCard(outputConfig({ initial_level: 1 })))
      expect(wrapper.get('.level-text').text()).toBe('HIGH')
    })
  })

  describe('input mode', () => {
    it('auto-reads level on mount and updates display', async () => {
      mocks.read.mockResolvedValue({ level: 1 })
      const wrapper = track(mountCard(inputConfig()))
      await flushPromises()

      expect(mocks.read).toHaveBeenCalledWith('node-1', 12)
      expect(wrapper.get('.level-text').text()).toBe('HIGH')
    })

    it('shows — before auto-read completes', () => {
      mocks.read.mockReturnValue(new Promise(() => {}))
      const wrapper = track(mountCard(inputConfig()))
      expect(wrapper.get('.level-text').text()).toBe('—')
    })

    it('does not auto-read when offline', async () => {
      mocks.read.mockResolvedValue({ level: 0 })
      const wrapper = track(mountCard(inputConfig(), true))
      await flushPromises()

      expect(mocks.read).not.toHaveBeenCalled()
      expect(wrapper.get('.level-text').text()).toBe('—')
    })

    it('silently ignores auto-read failure', async () => {
      mocks.read.mockRejectedValue(new Error('offline'))
      const wrapper = track(mountCard(inputConfig()))
      await flushPromises()

      expect(mocks.error).not.toHaveBeenCalled()
      expect(wrapper.get('.level-text').text()).toBe('—')
    })

    it('calls gpioApi.read on READ button click and updates level', async () => {
      mocks.read.mockResolvedValue({ level: 0 })
      const wrapper = track(mountCard(inputConfig()))
      await flushPromises()
      mocks.read.mockClear()

      const buttons = wrapper.findAll('button')
      const readBtn = buttons.find(b => b.text().includes('READ'))!
      await readBtn.trigger('click')
      await flushPromises()

      expect(mocks.read).toHaveBeenCalledWith('node-1', 12)
      expect(wrapper.get('.level-text').text()).toBe('LOW')
    })

    it('shows error message when read API fails on button click', async () => {
      mocks.read.mockResolvedValue({ level: 0 })
      const wrapper = track(mountCard(inputConfig()))
      await flushPromises()
      mocks.read.mockRejectedValue(new Error('timeout'))

      const buttons = wrapper.findAll('button')
      const readBtn = buttons.find(b => b.text().includes('READ'))!
      await readBtn.trigger('click')
      await flushPromises()

      expect(mocks.error).toHaveBeenCalledOnce()
    })
  })

  describe('offline state', () => {
    it('disables all output buttons when offline', () => {
      const wrapper = track(mountCard(outputConfig(), true))

      const buttons = wrapper.findAll('button')
      // ON, OFF, TOGGLE buttons (remove btn is also a button but has text ✕)
      const actionButtons = buttons.filter(b => !b.text().includes('✕'))
      actionButtons.forEach(btn => {
        expect((btn.element as HTMLButtonElement).disabled).toBe(true)
      })
    })

    it('disables READ button when offline', () => {
      const wrapper = track(mountCard(inputConfig(), true))

      const buttons = wrapper.findAll('button')
      const readBtn = buttons.find(b => b.text().includes('READ'))!
      expect((readBtn.element as HTMLButtonElement).disabled).toBe(true)
    })

    it('applies gpio-offline class', () => {
      const wrapper = track(mountCard(outputConfig(), true))
      expect(wrapper.find('.gpio-offline').exists()).toBe(true)
    })
  })

  describe('layout structure', () => {
    it('renders card with gpio-pin-card class and correct structure', () => {
      const wrapper = track(mountCard(outputConfig()))
      expect(wrapper.find('.gpio-pin-card').exists()).toBe(true)
      expect(wrapper.find('.card-header').exists()).toBe(true)
      expect(wrapper.find('.pin-name').exists()).toBe(true)
      expect(wrapper.find('.card-header-right').exists()).toBe(true)
    })

    it('renders action buttons in gpio-buttons container', () => {
      const wrapper = track(mountCard(outputConfig()))
      const btnContainer = wrapper.find('.gpio-buttons')
      expect(btnContainer.exists()).toBe(true)
      const buttons = btnContainer.findAll('button')
      expect(buttons.length).toBe(3) // ON, OFF, TOGGLE
    })

    it('renders level indicator with dot and text', () => {
      const wrapper = track(mountCard(outputConfig({ initial_level: 1 })))
      const indicator = wrapper.find('.gpio-level-indicator')
      expect(indicator.exists()).toBe(true)
      expect(indicator.find('.level-dot').exists()).toBe(true)
      expect(indicator.find('.level-text').exists()).toBe(true)
    })

    it('renders label when config has label', () => {
      const wrapper = track(mountCard(outputConfig({ label: 'LED Light' })))
      expect(wrapper.find('.pin-label').exists()).toBe(true)
      expect(wrapper.find('.pin-label').text()).toBe('LED Light')
    })

    it('does not render label when config has no label', () => {
      const wrapper = track(mountCard(outputConfig({ label: '' })))
      expect(wrapper.find('.pin-label').exists()).toBe(false)
    })
  })

  describe('remove emit', () => {
    it('emits remove with pin number on remove button click', async () => {
      const wrapper = track(mountCard(outputConfig({ pin: 7 })))

      const buttons = wrapper.findAll('button')
      const removeBtn = buttons.find(b => b.text().includes('✕'))!
      await removeBtn.trigger('click')

      expect(wrapper.emitted('remove')).toBeTruthy()
      expect(wrapper.emitted('remove')![0]).toEqual([7])
    })
  })
})
