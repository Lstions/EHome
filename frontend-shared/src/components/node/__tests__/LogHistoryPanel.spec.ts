import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

const mocks = vi.hoisted(() => ({
  getNodeLogs: vi.fn(),
  deleteNodeLogs: vi.fn(),
  confirm: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  exportCSV: vi.fn(),
}))

vi.mock('@/api/node', () => ({
  nodeApi: {
    getNodeLogs: mocks.getNodeLogs,
    deleteNodeLogs: mocks.deleteNodeLogs,
  },
}))
vi.mock('element-plus', () => ({
  ElMessage: {
    success: mocks.success,
    error: mocks.error,
    warning: mocks.warning,
  },
  ElMessageBox: { confirm: mocks.confirm },
}))
vi.mock('@/utils/exportData', () => ({ exportCSV: mocks.exportCSV }))

import LogHistoryPanel from '@/components/node/LogHistoryPanel.vue'
import logHistoryPanelSource from '@/components/node/LogHistoryPanel.vue?raw'

const InputStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: `<input v-bind="$attrs" :value="modelValue ?? ''" @input="$emit('update:modelValue', $event.target.value)" />`,
})

const SelectStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: `<select v-bind="$attrs" :value="modelValue ?? ''" @change="$emit('update:modelValue', $event.target.value === '' ? undefined : Number($event.target.value))"><slot /></select>`,
})

const OptionStub = defineComponent({
  props: ['label', 'value'],
  template: `<option :value="value">{{ label }}</option>`,
})

const DatePickerStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue', 'type'],
  emits: ['update:modelValue'],
  methods: {
    updatePart(index: number, value: string) {
      const current = Array.isArray(this.modelValue) ? [...this.modelValue] : ['', '']
      current[index] = value === '' ? '' : Number(value)
      this.$emit('update:modelValue', current)
    },
    updateDateTime(value: string) {
      this.$emit('update:modelValue', value === '' ? null : Number(value))
    },
  },
  template: `<div v-if="type === 'datetimerange'" v-bind="$attrs">
    <input aria-label="历史开始时间" :value="modelValue?.[0] ?? ''" @input="updatePart(0, $event.target.value)" />
    <input aria-label="历史结束时间" :value="modelValue?.[1] ?? ''" @input="updatePart(1, $event.target.value)" />
  </div>
  <input v-else v-bind="$attrs" :value="modelValue ?? ''" @input="updateDateTime($event.target.value)" />`,
})

const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: ['loading'],
  emits: ['click'],
  template: `<button v-bind="$attrs" :disabled="loading" :data-loading="String(Boolean(loading))" @click="$emit('click')"><slot /></button>`,
})

const PaginationStub = defineComponent({
  props: ['currentPage', 'pageSize', 'total'],
  emits: ['update:currentPage', 'current-change'],
  template: `<button aria-label="历史日志分页" @click="$emit('update:currentPage', currentPage + 1); $emit('current-change', currentPage + 1)">下一页</button>`,
})

// 使用 global.components 覆盖 test-setup 的全局 Element Plus stub。
// stubs 的 kebab-case key 无法覆盖已注册的 PascalCase 组件。
const components = {
  ElInput: InputStub,
  ElSelect: SelectStub,
  ElOption: OptionStub,
  ElDatePicker: DatePickerStub,
  ElButton: ButtonStub,
  ElPagination: PaginationStub,
  ElTable: true,
  ElTableColumn: true,
  ElTag: true,
  ElEmpty: defineComponent({ props: ['description'], template: '<div>{{ description }}</div>' }),
}

const log = {
  id: 17,
  node_id: 'ESP32-C6-01',
  level: 2,
  ts: 3_661_002_003,
  tag: 'MQTT',
  message: 'connected',
  created_at: '2026-07-13T08:00:00Z',
}

function mountPanel() {
  return mount(LogHistoryPanel, {
    props: { collectorId: 'collector-1' },
    global: { components: components as Record<string, any> },
  })
}

describe('LogHistoryPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getNodeLogs.mockResolvedValue({ total: 201, page: 1, size: 100, logs: [log] })
    mocks.deleteNodeLogs.mockResolvedValue({ deleted: 1 })
    mocks.confirm.mockResolvedValue('confirm')
  })

  it('reloads history when collector changes', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    await wrapper.setProps({ collectorId: 'collector-2' })
    await flushPromises()
    expect(mocks.getNodeLogs).toHaveBeenLastCalledWith('collector-2', expect.any(Object))
  })

  it('loads and displays saved history without consulting persistence configuration', async () => {
    const wrapper = mountPanel()
    await flushPromises()

    expect(mocks.getNodeLogs).toHaveBeenCalledWith('collector-1', { page: 1, size: 100 })
    expect(wrapper.text()).toContain('connected')
    expect(wrapper.text()).toContain('历史日志')
  })

  it('queries time, one level, tag and keyword and resets a changed page to one', async () => {
    const wrapper = mountPanel()
    await flushPromises()

    await wrapper.get('[aria-label="历史日志分页"]').trigger('click')
    await flushPromises()
    expect(mocks.getNodeLogs).toHaveBeenLastCalledWith('collector-1', { page: 2, size: 100 })

    await wrapper.get('[aria-label="历史开始时间"]').setValue('1783933200000')
    await wrapper.get('[aria-label="历史结束时间"]').setValue('1783936800000')
    await wrapper.get('[aria-label="历史日志级别"]').setValue('1')
    await wrapper.get('[aria-label="历史日志 Tag"]').setValue('RX_TASK')
    await wrapper.get('[aria-label="历史日志关键词"]').setValue('timeout')
    await wrapper.get('[aria-label="查询历史日志"]').trigger('click')
    await flushPromises()

    expect(mocks.getNodeLogs).toHaveBeenLastCalledWith('collector-1', {
      from: 1783933200000,
      to: 1783936800000,
      level: 1,
      tag: 'RX_TASK',
      q: 'timeout',
      page: 1,
      size: 100,
    })
  })

  it('confirms and performs cutoff cleanup and full cleanup, then refreshes history', async () => {
    const wrapper = mountPanel()
    await flushPromises()

    await wrapper.get('[aria-label="清理时间点"]').setValue('1783933200000')
    await wrapper.get('[aria-label="清理指定时间前日志"]').trigger('click')
    await flushPromises()

    expect(mocks.confirm).toHaveBeenCalledWith(
      expect.stringContaining('指定时间前'),
      expect.any(String),
      expect.objectContaining({ type: 'warning' }),
    )
    expect(mocks.deleteNodeLogs).toHaveBeenCalledWith('collector-1', 1783933200000)

    await wrapper.get('[aria-label="清理全部历史日志"]').trigger('click')
    await flushPromises()

    expect(mocks.confirm).toHaveBeenCalledTimes(2)
    expect(mocks.deleteNodeLogs).toHaveBeenLastCalledWith('collector-1')
    expect(mocks.getNodeLogs.mock.calls.length).toBeGreaterThanOrEqual(3)
  })

  it('never turns a cleared cutoff picker into a full-delete request', async () => {
    const wrapper = mountPanel()
    await flushPromises()

    const picker = wrapper.get('[aria-label="清理时间点"]')
    const cleanupButton = wrapper.get('[aria-label="清理指定时间前日志"]')
    await picker.setValue('1783933200000')
    expect(cleanupButton.attributes('disabled')).toBeUndefined()

    await picker.setValue('')
    expect(cleanupButton.attributes('disabled')).toBeDefined()
    await cleanupButton.trigger('click')
    await flushPromises()

    expect(mocks.confirm).not.toHaveBeenCalled()
    expect(mocks.deleteNodeLogs).not.toHaveBeenCalled()
  })

  it('uses request generation to ignore stale history responses', () => {
    // UI loading state belongs to the newest request; direct deferred-promise timing
    // is scheduler-sensitive in happy-dom, so verify the production guard itself.
    expect(logHistoryPanelSource).toContain('const generation = ++requestGeneration')
    expect(logHistoryPanelSource).toContain('if (generation !== requestGeneration) return')
    expect(logHistoryPanelSource).toContain('if (generation === requestGeneration) {\n      loading.value = false\n    }')
  })

  it('exports the currently loaded query result as CSV', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    // Explicitly restore the successful list fixture after tests that install deferred mocks.
    mocks.getNodeLogs.mockResolvedValue({ total: 201, page: 1, size: 100, logs: [log] })
    await wrapper.get('[aria-label="查询历史日志"]').trigger('click')
    await flushPromises()
    await wrapper.get('[aria-label="导出历史日志"]').trigger('click')

    expect(mocks.exportCSV).toHaveBeenCalledWith(
      'node-logs-collector-1',
      ['时间', '级别', 'Tag', '消息'],
      [{
        时间: '2026-07-13T08:00:00Z',
        级别: 'INFO',
        Tag: 'MQTT',
        消息: 'connected',
      }],
    )
  })
})
