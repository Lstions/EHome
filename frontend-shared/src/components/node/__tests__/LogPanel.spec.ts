import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, type PropType } from 'vue'
import type { RealtimeLogLine, RealtimeSearchCountState } from '@/components/node/logTypes'
import source from '../LogPanel.vue?raw'

const mocks = vi.hoisted(() => ({
  subscribe: vi.fn(),
  unsubscribe: vi.fn(),
  getLogConfig: vi.fn(),
  updateLogConfig: vi.fn(),
  updateLogPersist: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  loggerWarn: vi.fn(),
  exportCSV: vi.fn(),
  downloadText: vi.fn(),
  createObjectURL: vi.fn((_blob: Blob) => 'blob:realtime-log'),
  revokeObjectURL: vi.fn(),
}))

vi.mock('@/api/node', () => ({
  nodeApi: {
    getLogConfig: mocks.getLogConfig,
    updateLogConfig: mocks.updateLogConfig,
    updateLogPersist: mocks.updateLogPersist,
  },
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ subscribe: mocks.subscribe }),
}))
vi.mock('element-plus', () => ({
  ElMessage: { success: mocks.success, error: mocks.error, warning: mocks.warning },
}))
vi.mock('@/utils/logger', () => ({
  logger: { warn: mocks.loggerWarn },
}))
vi.mock('@/utils/exportData', () => ({
  exportCSV: mocks.exportCSV,
  downloadText: mocks.downloadText,
}))

import LogPanel from '@/components/node/LogPanel.vue'

const SwitchStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: Boolean },
  emits: ['update:modelValue', 'change'],
  template: `<button v-bind="$attrs" @click="$emit('update:modelValue', !modelValue); $emit('change', !modelValue)"><slot /></button>`,
})

const SelectStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue'],
  emits: ['update:modelValue', 'change'],
  template: `<select v-bind="$attrs" :value="modelValue" @change="$emit('update:modelValue', Number($event.target.value)); $emit('change', Number($event.target.value))"><slot /></select>`,
})

const OptionStub = defineComponent({
  props: ['label', 'value'],
  template: '<option :value="value">{{ label }}</option>',
})

const InputStub = defineComponent({
  inheritAttrs: false,
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: `<input v-bind="$attrs" :value="modelValue ?? ''" @input="$emit('update:modelValue', $event.target.value)" />`,
})

const RealtimeViewerStub = defineComponent({
  name: 'LogRealtimeViewer',
  props: {
    logs: { type: Array as PropType<RealtimeLogLine[]>, default: () => [] },
    receivedCount: { type: Number, default: 0 },
    generation: { type: Number, default: 0 },
    paused: Boolean,
    searchKeyword: { type: String, default: '' },
    searchCountState: {
      type: Object as PropType<RealtimeSearchCountState>,
      default: () => ({ epoch: 0, baselineId: 0, baselineMatchIds: [], matchedAfterBaseline: 0 }),
    },
  },
  emits: ['update:paused', 'update:searchKeyword', 'clear', 'export'],
  template: `<section aria-label="实时日志组件">
    <span data-testid="realtime-state">{{ logs.length }}|{{ receivedCount }}|{{ generation }}|{{ paused }}|{{ searchKeyword }}</span>
    <span data-testid="search-count-state">{{ searchCountState.epoch }}|{{ searchCountState.baselineId }}|{{ searchCountState.baselineMatchIds.length }}|{{ searchCountState.matchedAfterBaseline }}</span>
    <input aria-label="搜索实时日志" :value="searchKeyword" @input="$emit('update:searchKeyword', $event.target.value)" />
    <span v-for="line in logs" :key="line.id" class="received-log" :data-log-id="line.id">{{ line.msg }}</span>
    <button aria-label="切换暂停" @click="$emit('update:paused', !paused)">切换暂停</button>
    <button aria-label="清空实时日志" @click="$emit('clear')">清空</button>
    <button aria-label="导出实时文本" @click="$emit('export', 'text')">文本</button>
    <button aria-label="导出实时 CSV" @click="$emit('export', 'csv')">CSV</button>
  </section>`,
})

const HistoryPanelStub = defineComponent({
  name: 'LogHistoryPanel',
  props: ['collectorId'],
  template: '<section aria-label="历史日志组件">历史 {{ collectorId }}</section>',
})

const stubs = {
  'el-switch': SwitchStub,
  'el-select': SelectStub,
  'el-option': OptionStub,
  'el-alert': defineComponent({ template: '<div><slot /></div>' }),
  'el-input': InputStub,
  LogRealtimeViewer: RealtimeViewerStub,
  LogHistoryPanel: HistoryPanelStub,
}

function mountPanel(): VueWrapper {
  return mount(LogPanel, {
    props: { collectorId: 'node-1', nodeDeviceId: 'NODE-1' },
    global: { stubs },
  })
}

function sendLogEnvelope(nodeId: string, lines: Array<Omit<RealtimeLogLine, 'id'>>): void {
  wsHandler?.({
    type: 'node_log',
    payload: { node_id: nodeId, lines },
  })
}

let wsHandler: ((message: unknown) => void) | undefined

async function settlePanel(): Promise<VueWrapper> {
  const wrapper = mountPanel()
  await flushPromises()
  return wrapper
}

describe('LogPanel', () => {
  it('hands loading ownership to the new collector', () => {
    expect(source).toContain('operationGeneration++')
    expect(source).toContain('streamLoading.value = false')
    expect(source).toContain('persistLoading.value = false')
    expect(source).toContain('operation !== operationGeneration')
  })
  const wrappers: VueWrapper[] = []

  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getLogConfig.mockResolvedValue({ stream_enabled: true, persist_enabled: false, level: 2 })
    mocks.updateLogConfig.mockResolvedValue(undefined)
    mocks.updateLogPersist.mockResolvedValue(undefined)
    mocks.subscribe.mockImplementation((_event: string, handler: (message: unknown) => void) => {
      wsHandler = handler
      return mocks.unsubscribe
    })
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL: mocks.createObjectURL,
      revokeObjectURL: mocks.revokeObjectURL,
    })
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
  })

  afterEach(() => {
    wrappers.splice(0).forEach(wrapper => wrapper.unmount())
    wsHandler = undefined
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  const track = (wrapper: VueWrapper) => {
    wrappers.push(wrapper)
    return wrapper
  }

  it('reloads configuration when collector changes', async () => {
    const wrapper = track(await settlePanel())
    await wrapper.setProps({ collectorId: 'node-2', nodeDeviceId: 'NODE-2' })
    await flushPromises()
    expect(mocks.getLogConfig).toHaveBeenLastCalledWith('node-2')
    expect(wrapper.text()).toContain('历史 node-2')
  })

  it('loads configuration, subscribes with cleanup, and always exposes saved history', async () => {
    const wrapper = track(await settlePanel())

    expect(mocks.getLogConfig).toHaveBeenCalledWith('node-1')
    expect(mocks.subscribe).toHaveBeenCalledWith('node_log', expect.any(Function))
    expect(wrapper.find('[aria-label="实时日志组件"]').exists()).toBe(true)
    expect(wrapper.get('[aria-label="历史日志组件"]').text()).toContain('node-1')

    wrapper.unmount()
    wrappers.splice(wrappers.indexOf(wrapper), 1)
    expect(mocks.unsubscribe).toHaveBeenCalledOnce()
  })

  it('warns without blocking viewers when configuration loading fails', async () => {
    mocks.getLogConfig.mockRejectedValueOnce(new Error('network unavailable'))

    const wrapper = track(await settlePanel())

    expect(mocks.loggerWarn).toHaveBeenCalledWith('加载节点日志配置失败', {
      collectorId: 'node-1',
      error: 'network unavailable',
    })
    expect(mocks.warning).toHaveBeenCalledWith('日志配置加载失败，仍可查看实时与历史日志')
    expect(wrapper.find('[aria-label="实时日志组件"]').exists()).toBe(true)
    expect(wrapper.find('[aria-label="历史日志组件"]').exists()).toBe(true)
  })

  it('routes production envelopes only for the selected node and assigns stable increasing ids', async () => {
    const wrapper = track(await settlePanel())

    sendLogEnvelope('OTHER', [{ level: 2, ts: 1_000_000, tag: 'DROP', msg: 'ignored' }])
    sendLogEnvelope('NODE-1', [
      { level: 2, ts: 2_000_000, tag: 'MQTT', msg: 'connected' },
      { level: 1, ts: 3_000_000, tag: 'RX', msg: 'timeout' },
    ])
    await flushPromises()

    const logs = wrapper.findAll('.received-log')
    expect(logs.map(log => log.text())).toEqual(['connected', 'timeout'])
    expect(logs.map(log => log.attributes('data-log-id'))).toEqual(['1', '2'])
  })

  it('keeps a 5000-line bounded realtime buffer and preserves monotonic ids after trimming', async () => {
    const wrapper = track(await settlePanel())
    const lines = Array.from({ length: 5002 }, (_, index) => ({
      level: index % 5,
      ts: index * 1_000_000,
      tag: 'LOAD',
      msg: `line-${index}`,
    }))

    sendLogEnvelope('NODE-1', lines)
    await flushPromises()

    const logs = wrapper.findAll('.received-log')
    expect(logs).toHaveLength(5000)
    expect(logs[0].text()).toBe('line-2')
    expect(logs[0].attributes('data-log-id')).toBe('3')
    expect(logs[logs.length - 1].attributes('data-log-id')).toBe('5002')
    expect(wrapper.get('[data-testid="realtime-state"]').text()).toBe('5000|5002|0|false|')

    sendLogEnvelope('NODE-1', Array.from({ length: 7 }, (_, index) => ({
      level: 2,
      ts: index,
      tag: 'AFTER_CAP',
      msg: `after-cap-${index}`,
    })))
    await flushPromises()
    expect(wrapper.get('[data-testid="realtime-state"]').text()).toBe('5000|5009|0|false|')
  })

  it('continues buffering while paused and passes search state to the viewer', async () => {
    const wrapper = track(await settlePanel())

    await wrapper.get('[aria-label="切换暂停"]').trigger('click')
    await wrapper.get('[aria-label="搜索实时日志"]').setValue('timeout')
    sendLogEnvelope('NODE-1', [{ level: 1, ts: 4_000_000, tag: 'RX', msg: 'timeout while paused' }])
    await flushPromises()

    expect(wrapper.get('[data-testid="realtime-state"]').text()).toBe('1|1|0|true|timeout')
    expect(wrapper.text()).toContain('timeout while paused')
  })

  it('lets the realtime viewer render search and owns the updated search state', async () => {
    const wrapper = track(await settlePanel())

    expect(wrapper.find('.realtime-search').exists()).toBe(false)
    await wrapper.get('[aria-label="搜索实时日志"]').setValue('sensor')
    await flushPromises()

    expect(wrapper.get('[data-testid="realtime-state"]').text()).toBe('0|0|0|false|sensor')
    expect(wrapper.get('[data-testid="search-count-state"]').text()).toBe('1|0|0|0')
  })

  it('clears through the child event without reusing ids for later logs', async () => {
    const wrapper = track(await settlePanel())

    sendLogEnvelope('NODE-1', [{ level: 2, ts: 1, tag: 'A', msg: 'before clear' }])
    await flushPromises()
    await wrapper.get('[aria-label="清空实时日志"]').trigger('click')
    expect(wrapper.get('[data-testid="realtime-state"]').text()).toBe('0|1|1|false|')
    sendLogEnvelope('NODE-1', [{ level: 2, ts: 2, tag: 'B', msg: 'after clear' }])
    await flushPromises()

    const logs = wrapper.findAll('.received-log')
    expect(logs).toHaveLength(1)
    expect(logs[0].text()).toBe('after clear')
    expect(logs[0].attributes('data-log-id')).toBe('2')
    expect(wrapper.get('[data-testid="realtime-state"]').text()).toBe('1|2|1|false|')
  })

  it('publishes a clear generation with logs arriving in the same Vue batch', async () => {
    const wrapper = track(await settlePanel())
    sendLogEnvelope('NODE-1', [{ level: 2, ts: 1, tag: 'A', msg: 'before clear' }])
    await flushPromises()

    const clearPromise = wrapper.get('[aria-label="清空实时日志"]').trigger('click')
    sendLogEnvelope('NODE-1', [
      { level: 2, ts: 2, tag: 'B', msg: 'same tick one' },
      { level: 2, ts: 3, tag: 'B', msg: 'same tick two' },
    ])
    await clearPromise
    await flushPromises()

    expect(wrapper.get('[data-testid="realtime-state"]').text()).toBe('2|3|1|false|')
    expect(wrapper.findAll('.received-log').map(log => log.attributes('data-log-id'))).toEqual(['2', '3'])
  })

  it('performs filtered CSV and text downloads for child export events', async () => {
    const wrapper = track(await settlePanel())

    sendLogEnvelope('NODE-1', [
      { level: 2, ts: 1_000_000, tag: 'KEEP', msg: 'kept message' },
      { level: 0, ts: 2_000_000, tag: 'DROP', msg: 'other message' },
    ])
    await wrapper.get('[aria-label="搜索实时日志"]').setValue('keep')
    await flushPromises()

    await wrapper.get('[aria-label="导出实时 CSV"]').trigger('click')
    expect(mocks.exportCSV).toHaveBeenCalledWith(
      'realtime-logs-node-1',
      ['运行时间', '级别', 'Tag', '消息'],
      [{ 运行时间: '00:00:01.000', 级别: 'INFO', Tag: 'KEEP', 消息: 'kept message' }],
    )

    await wrapper.get('[aria-label="导出实时文本"]').trigger('click')
    expect(mocks.downloadText).toHaveBeenCalledWith(
      '00:00:01.000 INFO KEEP kept message',
      'realtime-logs-node-1.txt',
    )
  })

  it('keeps receivedCount as a full monotonic arrival sequence independent of search', async () => {
    const wrapper = track(await settlePanel())
    await wrapper.get('[aria-label="搜索实时日志"]').setValue('keep')

    sendLogEnvelope('NODE-1', [
      { level: 2, ts: 1, tag: 'DROP', msg: 'hidden' },
      { level: 2, ts: 2, tag: 'KEEP', msg: 'visible' },
    ])
    await flushPromises()

    expect(wrapper.get('[data-testid="realtime-state"]').text()).toBe('2|2|0|false|keep')
  })

  it('publishes bounded search metadata that counts matches across trimming', async () => {
    const wrapper = track(await settlePanel())
    await wrapper.get('[aria-label="搜索实时日志"]').setValue('match')

    const lines = Array.from({ length: 6000 }, (_, index) => ({
      level: 2,
      ts: index,
      tag: index % 2 === 0 ? 'MATCH' : 'OTHER',
      msg: `line-${index}`,
    }))
    sendLogEnvelope('NODE-1', lines)
    await flushPromises()

    expect(wrapper.get('[data-testid="realtime-state"]').text()).toBe('5000|6000|0|false|match')
    expect(wrapper.get('[data-testid="search-count-state"]').text()).toBe('1|0|0|3000')
  })

  it('rebases a changed search from only the retained window', async () => {
    const wrapper = track(await settlePanel())
    await wrapper.get('[aria-label="搜索实时日志"]').setValue('first')

    sendLogEnvelope('NODE-1', Array.from({ length: 6000 }, (_, index) => ({
      level: 2,
      ts: index,
      tag: index % 2 === 0 ? 'FIRST' : index % 4 === 1 ? 'SECOND' : 'OTHER',
      msg: `line-${index}`,
    })))
    await flushPromises()

    await wrapper.get('[aria-label="搜索实时日志"]').setValue('second')
    await flushPromises()

    expect(wrapper.get('[data-testid="search-count-state"]').text()).toBe('2|6000|1250|0')
  })

  it('rebases active search metadata on clear before same-batch arrivals', async () => {
    const wrapper = track(await settlePanel())
    await wrapper.get('[aria-label="搜索实时日志"]').setValue('match')
    sendLogEnvelope('NODE-1', [{ level: 2, ts: 1, tag: 'MATCH', msg: 'before clear' }])
    await flushPromises()

    const clearPromise = wrapper.get('[aria-label="清空实时日志"]').trigger('click')
    sendLogEnvelope('NODE-1', [
      { level: 2, ts: 2, tag: 'MATCH', msg: 'same batch match' },
      { level: 2, ts: 3, tag: 'OTHER', msg: 'same batch miss' },
    ])
    await clearPromise
    await flushPromises()

    expect(wrapper.get('[data-testid="search-count-state"]').text()).toBe('2|1|0|1')
  })

  it('updates stream, level, and persistence configuration through DOM controls', async () => {
    const wrapper = track(await settlePanel())

    await wrapper.get('[aria-label="日志级别"]').setValue('3')
    await wrapper.get('[aria-label="日志流开关"]').trigger('click')
    await wrapper.get('[aria-label="日志持久化开关"]').trigger('click')
    await flushPromises()

    expect(mocks.updateLogConfig).toHaveBeenCalledWith('node-1', { stream_enabled: false })
    expect(mocks.updateLogConfig).toHaveBeenCalledWith('node-1', { level: 3 })
    expect(mocks.updateLogPersist).toHaveBeenCalledWith('node-1', true)
  })
})
