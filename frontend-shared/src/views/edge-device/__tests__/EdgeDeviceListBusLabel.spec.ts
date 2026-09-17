import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import EdgeDeviceList from '@/views/edge-device/EdgeDeviceList.vue'
import source from '@/views/edge-device/EdgeDeviceList.vue?raw'
import type { EdgeDevice } from '@/api/edgeDevice'

// 缺陷回归（2026-09-17 生产实测，同一缺陷类的**剩余 3 处**，全在 EdgeDeviceList.vue）：
//   真实挂在 **UART0** 总线上的雨量计，在「列表表格 / 移动端卡片 / 批量导出 CSV」三处
//   都被显示成 "UART 1"。
//
// 根因：这三处拼的是 **设备级** hardware_type + hardware_id —— 而边缘设备上
//   · hardware_type —— 设备上为空/无意义；
//   · hardware_id   —— **Modbus/I2C 从站地址**（雨量计 = "1"、"0x76"），不是总线名。
//   真正的总线名在**关联通道**上：device.channel_hardware_id（= channel.hardware_id，"UART0"）。
//
// 本文件守住的三个不变量（与 DeviceDeleteDialogChannel.spec.ts 同一口径）：
//   ① 有 channel 时显示真实总线名 UART0；
//   ② 无 channel（老数据/紧凑列表）时回退为 UNKNOWN '—'，不崩、不显示地址；
//   ③ 回归点：hardware_id="1" + channel_hardware_id="UART0" 时，三处都绝不出现 "UART 1"。
//
// 注 1：normalize() → 列表紧凑路径保留 channel_hardware_id 由
//       components/device/__tests__/DeviceDeleteDialogChannelNormalize.spec.ts 覆盖；
//       本文件专注**视图层**（EdgeDeviceList.vue）的三处渲染口径。
// 注 2：test-setup.ts 的共享 ElTable stub 只渲染 Object.values(row).join(' ')，**不渲染**
//       列插槽内容 —— 拿它测「总线」列等于测空气。这里局部覆盖 ElTable/ElTableColumn
//       为真正求值列插槽的探针，使表格列回归是真实渲染断言（不是源码字符串断言）。

const { mockEdgeDeviceGetList, mockGetLogicalDeviceInfo, mockGetDriverCommands } = vi.hoisted(() => ({
  mockEdgeDeviceGetList: vi.fn<() => Promise<{ items: unknown[]; total: number }>>(() => Promise.resolve({ items: [], total: 0 })),
  mockGetLogicalDeviceInfo: vi.fn(() => new Promise(() => {})),
  mockGetDriverCommands: vi.fn((..._args: unknown[]) => Promise.resolve([])),
}))

vi.mock('element-plus', () => ({
  ElMessage: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))
vi.mock('@/stores/node', () => ({
  useNodeStore: () => ({
    fetchNodes: vi.fn(() => Promise.resolve()),
    getCachedList: vi.fn(() => ({ items: [{ id: 1, node_id: 'node-1', name: 'Collector-A', status: 'online' }], total: 1 })),
  }),
}))
vi.mock('@/stores/websocket', () => ({
  useWebSocketStore: () => ({ connected: false, connect: vi.fn(), subscribe: vi.fn(() => vi.fn()) }),
}))
vi.mock('@/api/edgeDevice', () => ({
  // 紧凑列表只做形状过滤，字段原样透传（含 channel_hardware_id / channel）
  compactEdgeDeviceList: (items: unknown) =>
    Array.isArray(items) ? items.filter(item => item && typeof item === 'object' && 'id' in item) : [],
  edgeDeviceApi: {
    getList: mockEdgeDeviceGetList,
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    getLogicalDeviceInfo: mockGetLogicalDeviceInfo,
    getCandidates: vi.fn(() => Promise.resolve([])),
    getDriverCommands: mockGetDriverCommands,
  },
}))
vi.mock('@/api/channel', () => ({
  compactChannelList: (items: unknown) => (Array.isArray(items) ? items.filter(item => item && typeof item === 'object') : []),
  channelApi: { getList: vi.fn(() => Promise.resolve([])), update: vi.fn() },
}))
vi.mock('@/api/deviceConfig', () => ({ deviceConfigApi: { getList: vi.fn(() => Promise.resolve({ list: [] })) } }))
vi.mock('@/api/parser', () => ({ parserApi: { getList: vi.fn(() => Promise.resolve([])) } }))
vi.mock('@/api/client', () => ({ default: { get: vi.fn(() => Promise.resolve({ data_count_today: 0 })) } }))

/** 永不渲染的列定义占位（真正渲染由 TableProbe 求值列插槽） */
const ElTableColumnProbe = defineComponent({
  props: { label: String, prop: String, type: String, width: [String, Number], minWidth: [String, Number], fixed: [String, Boolean] },
  setup: () => () => null,
})

/**
 * 真正求值 `#default="{ row }"` 列插槽的 el-table 探针。
 * 共享 stub 只把整行对象 join 成文本，任何列插槽都不求值 —— 那样的"表格测试"是假绿。
 */
const ElTableProbe = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  setup(props, { slots }) {
    return () => {
      const columns = (slots.default?.() ?? []) as any[]
      return h('table', { class: 'el-table' }, [
        h('tbody', props.data.map((row: any, index: number) => h('tr', { class: 'el-table__row' }, columns
          .filter((col: any) => col && col.type)
          .map((col: any) => {
            const label = col.props?.label ?? col.props?.prop ?? ''
            const cellSlot = col.children && typeof col.children === 'object' ? col.children.default : undefined
            const content = cellSlot ? cellSlot({ row, $index: index }) : []
            return h('td', { class: 'el-table__cell', 'data-label': label }, content)
          })))),
      ])
    }
  },
})

const stubs = {
  SkeletonCard: { template: '<div data-testid="skeleton-card" />' },
  EmptyState: { template: '<div data-testid="empty-state" />' },
  CountUp: { template: '<span data-testid="count-up">{{ $attrs.value }}</span>' },
  ElTable: ElTableProbe,
  ElTableColumn: ElTableColumnProbe,
}

/**
 * 生产实测的设备：测试雨量计。
 * hardware_type='uart' + hardware_id='1' 正是旧写法拼出 "UART 1" 的两个输入。
 */
function rainGauge(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 7,
    node_id: 1,
    name: '测试雨量计',
    status: 'active',
    device_type: 'sn3001_rain',
    protocol: 'modbus',
    hardware_type: 'uart',
    hardware_id: '1',
    node: { id: 1, name: 'Collector-A' },
    ...overrides,
  }
}

const WITH_CHANNEL = rainGauge({
  channel_hardware_id: 'UART0',
  channel: { hardware_type: 'UART', hardware_id: 'UART0', bus_type: 'UART' },
})
// 老数据 / 紧凑列表：后端没给 channel —— 此时**不能**拿设备地址冒充总线名
const WITHOUT_CHANNEL = rainGauge({ hardware_type: 'uart', hardware_id: '1' })

async function mountWith(device: Record<string, unknown>, mode: 'card' | 'table') {
  mockEdgeDeviceGetList.mockResolvedValue({ items: [device], total: 1 })
  const wrapper = mount(EdgeDeviceList, { global: { stubs } })
  await flushPromises()
  ;(wrapper.vm as any).viewMode = mode
  await flushPromises()
  return wrapper
}

/** 按标签定位卡片里的「总线通道」值，避免误读页面上其它文本。 */
function cardBusText(wrapper: ReturnType<typeof mount>): string {
  const item = wrapper.findAll('.device-card .fact-item').find(i => i.find('.fact-label').text() === '总线通道')
  expect(item, '未找到「总线通道」项 —— 选择器失效，断言将形同虚设').toBeTruthy()
  return item!.find('code').text()
}

/** 按列标签定位表格「总线」单元格。 */
function tableBusText(wrapper: ReturnType<typeof mount>): string {
  const cell = wrapper.find('td[data-label="总线"]')
  expect(cell.exists(), '未找到「总线」列 —— 探针失效，断言将形同虚设').toBe(true)
  return cell.text()
}

// ── CSV 捕获 ──
let csvTextPromise: Promise<string> | null = null
const originalCreateObjectURL = (URL as any).createObjectURL
const originalRevokeObjectURL = (URL as any).revokeObjectURL

function installCsvCapture() {
  csvTextPromise = null
  ;(URL as any).createObjectURL = (blob: Blob) => {
    csvTextPromise = blob.text()
    return 'blob:mock-url'
  }
  ;(URL as any).revokeObjectURL = () => {}
}

/** 取 CSV 第 2 行（首条数据）的第 5 列 —— 表头 ['ID','名称','类型','节点','总线',...] */
function csvBusCell(csv: string): string {
  return csv.split('\n')[1].split(',').map(c => c.replace(/^"|"$/g, ''))[4]
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  localStorage.clear()
  sessionStorage.clear()
  installCsvCapture()
})

afterEach(() => {
  ;(URL as any).createObjectURL = originalCreateObjectURL
  ;(URL as any).revokeObjectURL = originalRevokeObjectURL
})

describe('EdgeDeviceList 总线列 = 关联通道总线名（回归 "UART 1" 显示缺陷 · 剩余 3 处）', () => {
  it('源码级：3 处总线渲染点全部走 getDeviceBusLabel，且全文再无 hardware_type + hardware_id 拼接', () => {
    // ① 表格「总线」列 / ② 移动端卡片「总线通道」/ ③ CSV「总线」列 —— 恰好 3 个调用点
    expect(source.match(/getDeviceBusLabel\(/g) ?? []).toHaveLength(3)
    expect(source).toContain('{{ getDeviceBusLabel(asDevice(row)) }}')
    expect(source).toContain('{{ getDeviceBusLabel(device) }}')
    // CSV 行：不得再是模板串拼接
    expect(source).not.toContain('`${device.hardware_type?.toUpperCase()} ${device.hardware_id}`')
    // 反向钉死：旧写法（row/device 的 hardware_type + hardware_id 拼接）任何形态都不得复活
    expect(source).not.toMatch(/hardware_type\?\.toUpperCase\(\)[^\n]{0,20}hardware_id/)
    // 取值口径本身：优先 channel_hardware_id → channel.hardware_id → UNKNOWN('—')
    expect(source).toContain('device.channel_hardware_id || device.channel?.hardware_id || UNKNOWN')
  })

  it('表格「总线」列：显示通道总线名 UART0，绝不显示 "UART 1"', async () => {
    const wrapper = await mountWith(WITH_CHANNEL, 'table')

    expect(tableBusText(wrapper)).toBe('UART0')
    expect(wrapper.text()).not.toContain('UART 1')
  })

  it('移动端卡片「总线通道」：显示通道总线名 UART0，绝不显示 "UART 1"', async () => {
    const wrapper = await mountWith(WITH_CHANNEL, 'card')

    expect(cardBusText(wrapper)).toBe('UART0')
    expect(wrapper.text()).not.toContain('UART 1')
  })

  it('批量导出 CSV「总线」列：输出 UART0 而不是 "UART 1"', async () => {
    const wrapper = await mountWith(WITH_CHANNEL, 'table')
    const vm = wrapper.vm as any
    vm.selectedDevices = [WITH_CHANNEL as unknown as EdgeDevice]

    vm.handleBatchExport()
    expect(csvTextPromise, '未捕获到 CSV Blob —— 导出路径没有被执行').toBeTruthy()
    const csv = await csvTextPromise!

    expect(csv).not.toContain('UART 1')
    expect(csvBusCell(csv)).toBe('UART0')
  })

  it('缺失通道（老数据/紧凑列表）：三处都回退为 — 而非把设备地址当总线名', async () => {
    const wrapper = await mountWith(WITHOUT_CHANNEL, 'card')
    expect(cardBusText(wrapper)).toBe('—')
    expect(wrapper.text()).not.toContain('UART 1')

    ;(wrapper.vm as any).viewMode = 'table'
    await flushPromises()
    expect(tableBusText(wrapper)).toBe('—')

    const vm = wrapper.vm as any
    vm.selectedDevices = [WITHOUT_CHANNEL as unknown as EdgeDevice]
    vm.handleBatchExport()
    const csv = await csvTextPromise!
    expect(csvBusCell(csv)).toBe('—')
    expect(csv).not.toContain('UART 1')
  })

  it('仅有 channel 对象（未显式给 channel_hardware_id）时也读 channel.hardware_id', async () => {
    const wrapper = await mountWith(rainGauge({
      channel: { hardware_type: 'I2C', hardware_id: 'I2C0' },
    }), 'card')

    expect(cardBusText(wrapper)).toBe('I2C0')
  })

  it('channel_hardware_id 为空串（而非 undefined）时同样回退为 —', async () => {
    const wrapper = await mountWith(rainGauge({
      channel_hardware_id: '',
      channel: { hardware_type: 'UART', hardware_id: '' },
    }), 'card')

    expect(cardBusText(wrapper)).toBe('—')
    expect(wrapper.text()).not.toContain('UART 1')
  })
})
