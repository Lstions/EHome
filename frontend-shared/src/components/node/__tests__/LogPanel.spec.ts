import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent } from 'vue'

const mocks = vi.hoisted(() => ({
  subscribe: vi.fn(),
  unsubscribe: vi.fn(),
  getLogConfig: vi.fn(),
  updateLogConfig: vi.fn(),
  updateLogPersist: vi.fn(),
  getNodeLogs: vi.fn(),
  deleteNodeLogs: vi.fn(),
}))

vi.mock('@/api/node', () => ({
  nodeApi: {
    getLogConfig: mocks.getLogConfig,
    updateLogConfig: mocks.updateLogConfig,
    updateLogPersist: mocks.updateLogPersist,
    getNodeLogs: mocks.getNodeLogs,
    deleteNodeLogs: mocks.deleteNodeLogs,
  },
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ subscribe: mocks.subscribe }),
}))
vi.mock('element-plus', () => ({
  ElMessage: { success: vi.fn(), error: vi.fn() },
  ElMessageBox: { confirm: vi.fn() },
}))

import LogPanel from '@/components/node/LogPanel.vue'

const stubs = {
  'el-switch': defineComponent({ emits: ['change'], template: '<button @click="$emit(\'change\', true)"><slot /></button>' }),
  'el-select': defineComponent({ template: '<div><slot /></div>' }),
  'el-option': true,
  'el-alert': true,
  'el-empty': true,
  'el-button': defineComponent({ emits: ['click'], template: '<button @click="$emit(\'click\')"><slot /></button>' }),
  'el-icon': true,
  'el-input': true,
  'el-table': true,
  'el-table-column': true,
  'el-tag': true,
  'el-pagination': true,
  VideoPause: true,
  VideoPlay: true,
}

function mountPanel() {
  return mount(LogPanel, {
    props: { collectorId: 'node-1', nodeDeviceId: 'NODE-1' },
    global: { stubs },
  })
}

describe('LogPanel', () => {
  let wsHandler: ((message: any) => void) | undefined

  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getLogConfig.mockResolvedValue({ stream_enabled: true, persist_enabled: true, level: 2 })
    mocks.getNodeLogs.mockResolvedValue({ logs: [], total: 0 })
    mocks.subscribe.mockImplementation((_event: string, handler: (message: any) => void) => {
      wsHandler = handler
      return mocks.unsubscribe
    })
  })

  afterEach(() => { wsHandler = undefined })

  it('loads config and subscribes to NODE_LOG with cleanup', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    expect(mocks.getLogConfig).toHaveBeenCalledWith('node-1')
    expect(mocks.subscribe).toHaveBeenCalledWith('node_log', expect.any(Function))
    expect(wrapper.text()).toContain('实时日志')
    wrapper.unmount()
    expect(mocks.unsubscribe).toHaveBeenCalledOnce()
  })

  it('accepts only matching node logs and renders uptime instead of epoch time', async () => {
    const wrapper = mountPanel()
    await flushPromises()
    wsHandler?.({ data: { node_id: 'OTHER', lines: [{ level: 2, ts: 1_000_000, tag: 'DROP', msg: 'ignored' }] } })
    wsHandler?.({ data: { node_id: 'NODE-1', lines: [{ level: 2, ts: 3_661_002_003, tag: 'MQTT', msg: 'connected' }] } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('ignored')
    expect(wrapper.text()).toContain('connected')
    expect(wrapper.text()).toContain('运行 01:01:01.002')
    expect(wrapper.text()).not.toContain('1970')
  })

  it('updates stream, level, persistence and queries history through node API', async () => {
    mocks.updateLogConfig.mockResolvedValue({})
    mocks.updateLogPersist.mockResolvedValue({})
    mocks.getNodeLogs.mockResolvedValue({ logs: [{ tag: 'RX', message: 'sample', created_at: '2026-07-12T12:00:00Z', level: 2 }], total: 1 })
    const wrapper = mountPanel()
    await flushPromises()

    await (wrapper.vm as any).onStreamToggle(false)
    await (wrapper.vm as any).onLevelChange(3)
    await (wrapper.vm as any).onPersistToggle(true)
    await (wrapper.vm as any).queryLogs()

    expect(mocks.updateLogConfig).toHaveBeenCalledWith('node-1', { stream_enabled: false })
    expect(mocks.updateLogConfig).toHaveBeenCalledWith('node-1', { level: 3 })
    expect(mocks.updateLogPersist).toHaveBeenCalledWith('node-1', true)
    expect(mocks.getNodeLogs).toHaveBeenCalledWith('node-1', expect.objectContaining({ page: 1, size: 100 }))
  })
})
