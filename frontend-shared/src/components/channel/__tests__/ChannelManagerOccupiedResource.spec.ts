/**
 * 缺陷 P0（2026-10-03）：前端「硬件资源」下拉框不排除**已被其它通道占用**的资源。
 *
 * 实测证据（生产）：UART1 上已有通道，用户在向导里仍能再选 UART1 并提交成功
 * （HTTP 201，见后端 channel_cross_pin_test.go 的同一缺陷）。两条通道认领同一对
 * 引脚后，节点端 manifest 权威校验拒收**整份** manifest ⇒ 该节点所有指令失效。
 *
 * 后端现在会以 409 拒绝，但让用户填完整张表再被拒是坏体验 —— 本文件锁住
 * "已占用的资源在下拉框里被禁用并标注"。
 *
 * 断言口径：沿用 test-setup.ts 的全局 ElOption stub（渲染为 option.el-option，
 * disabled 直通 DOM），所以这里查的是**真实渲染出来的 DOM 属性**，
 * 不是"源码里有这行字"。
 *
 * 这些用例凭什么会失败：把模板里的 :disabled="isOccupiedResource(hw)" 去掉，
 * "已占用被禁用"用例立刻红（disabled 变 undefined）。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import source from '../ChannelManager.vue?raw'

const mocks = vi.hoisted(() => ({
  getList: vi.fn().mockResolvedValue({ data: { items: [], total: 0 } }),
}))

vi.mock('@/api/channel', () => ({ channelApi: { create: vi.fn(), update: vi.fn(), getList: mocks.getList } }))
vi.mock('@/stores/channel', () => ({ useChannelStore: () => ({ fetchChannels: vi.fn() }) }))

import ChannelManager from '@/components/channel/ChannelManager.vue'

function caps() {
  return {
    buses: {
      uart: [
        { id: 'UART0', name: 'UART0', default_tx_pin: 43, default_rx_pin: 44, max_baud: 5000000 },
        { id: 'UART1', name: 'UART1', default_tx_pin: 4, default_rx_pin: 5, max_baud: 5000000 },
      ],
    },
  }
}

/** 硬件资源下拉框 = 第二个 el-select（第一个是硬件类型）。 */
function hardwareOptions(wrapper: ReturnType<typeof mount>) {
  const selects = wrapper.findAll('select')
  const hwSelect = selects[1]
  return new Map(
    hwSelect.findAll('option')
      .filter(o => o.attributes('value') !== '')
      .map(o => [o.attributes('value'), o] as const),
  )
}

async function mountManager(props: Record<string, unknown>) {
  const wrapper = mount(ChannelManager as any, {
    props: { modelValue: false, collectorId: 'node-1', presetHardwareType: 'uart', ...props },
  })
  await wrapper.setProps({ modelValue: true })
  await flushPromises()
  return wrapper
}

beforeEach(() => vi.clearAllMocks())

describe('ChannelManager 硬件资源下拉框必须标出已占用资源（2026-10-03 P0）', () => {
  it('源码级：模板对选项应用 isOccupiedResource 禁用并有可见提示', () => {
    const code = source
      .replace(/<!--[\s\S]*?-->/g, '')
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .replace(/^\s*\/\/.*$/gm, '')
    expect(code, '下拉框必须按占用状态禁用选项（否则用户能重复选同一个串口）')
      .toContain(':disabled="isOccupiedResource(hw)"')
    expect(code, '已占用必须有可见提示').toContain('已被其它通道占用')
  })

  it('源码级：占用判定必须排除编辑中的通道自身', () => {
    const code = source.replace(/^\s*\/\/.*$/gm, '')
    // 不排除自身 ⇒ 编辑一条 UART1 通道时 UART1 被标灰，连原样保存都做不到。
    expect(code, 'isOccupiedResource 必须比对 initialData.hardware_id 排除自身')
      .toContain('props.initialData?.hardware_id')
  })

  it('行为级：已占用资源被禁用，未占用资源仍可选', async () => {
    const wrapper = await mountManager({ capabilities: caps(), occupiedHardwareIds: ['UART1'] })
    const opts = hardwareOptions(wrapper)
    expect(opts.size, '应渲染出两个硬件资源选项').toBe(2)
    expect(opts.get('UART1')!.attributes('disabled'), 'UART1 已被其它通道占用，必须禁用').toBeDefined()
    expect(opts.get('UART0')!.attributes('disabled'), 'UART0 未被占用，必须仍可选').toBeUndefined()
  })

  it('行为级：大小写不一致也要判定为占用（能力 id 与历史数据曾出现大小写差异）', async () => {
    const wrapper = await mountManager({ capabilities: caps(), occupiedHardwareIds: ['uart1'] })
    expect(hardwareOptions(wrapper).get('UART1')!.attributes('disabled'), '占用判定必须大小写不敏感').toBeDefined()
  })

  it('行为级：没有任何占用时两个选项都可选（防加严过头）', async () => {
    const wrapper = await mountManager({ capabilities: caps(), occupiedHardwareIds: [] })
    for (const option of hardwareOptions(wrapper).values()) {
      expect(option.attributes('disabled')).toBeUndefined()
    }
  })

  it('行为级：编辑自身通道时不得把自己标成已占用', async () => {
    const wrapper = await mountManager({
      capabilities: caps(),
      occupiedHardwareIds: ['UART1'],
      initialData: { id: 7, node_id: 'node-1', hardware_type: 'UART', bus_type: 'UART', hardware_id: 'UART1', bus_config: '04050000258001', enabled: true, interval_ms: 5000, config: {} },
    })
    expect(
      hardwareOptions(wrapper).get('UART1')!.attributes('disabled'),
      '编辑 UART1 时 UART1 不能被自己挡住（否则连原样保存都做不到）',
    ).toBeUndefined()
  })
})
