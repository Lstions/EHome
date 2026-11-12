import type { Capabilities, DmaChannelInfo } from '@/api/node'
import type { Channel } from '@/api/channel'
import { DmaState, isDmaRebindable } from '@/utils/dmaState'
import { baudCurrentRate } from './channelDisplay'

/**
 * 总线资源（设备上报的只读能力）的纯函数层。
 *
 * 为什么单独成文件：这些函数是「资源视角」的唯一口径 —— 资源表、资源详情、
 * 「已创建通道」卡、波特率范围都从同一套 id 归并与掩码判断派生。页面与子组件
 * 都需要它们，但都不该各自复制一份（复制出来的第二份迟早与资源表分叉，这正是
 * 历史上「同一个资源在 DMA 卡里写成 uart/UART0、在资源表里写成 UART0」的成因）。
 *
 * 注意：原先这些函数直接读页面级的 activeBusType / channels / dmaStore 单例状态，
 * 这里改为**显式参数**，让调用方传入当前上下文；函数体逐行未改。
 */

export type BusType = 'i2c' | 'uart' | 'spi' | 'adc' | 'gpio' | 'pwm'
export type BusResource = Record<string, any> & { id: string; enabled?: boolean }

// 位掩码表只覆盖有掩码位的总线类型；gpio/pwm/adc 无掩码位（索引结果为 undefined → 走 || 0）。
export const BUS_TYPE_MASKS: Partial<Record<BusType, number>> = { uart: 1, i2c: 2, spi: 4 }

export function busTypeMask(type: BusType): number {
  return BUS_TYPE_MASKS[type] || 0
}

/**
 * DMA 能力掩码 → 总线名列表。
 *
 * 2026-10-02 统一分隔符：改前用半角 ', '，而本页其它并列项（关键参数、能力位）
 * 用的是 ' · '。同一个页面里两种并列符属于纯噪声，这里统一为 ' · '。
 */
export function busText(bus: number): string {
  const parts: string[] = []
  if (bus & 1) parts.push('UART')
  if (bus & 2) parts.push('I2C')
  if (bus & 4) parts.push('SPI')
  return parts.join(' · ') || '无'
}

export function dmaTypeText(type: number): string {
  return type === 0 ? 'GDMA' : `类型${type}`
}

/**
 * DMA 能力位的中文文案。
 *
 * 2026-10-02 统一：改前是 'TX' / 'RX' / 'Burst' —— 前两个是缩写、第三个是英文单词，
 * 混在同一行里既不同语言也不同粒度；而同一张卡上「最大突发」用的是中文。
 * 现在三项都用中文，与卡片内其它字段一致。
 */
export function capText(cap: number): string {
  const parts: string[] = []
  if (cap & 1) parts.push('发送')
  if (cap & 2) parts.push('接收')
  if (cap & 4) parts.push('突发')
  return parts.join(' · ') || '无'
}

/**
 * DMA 通道绑定的展示文案。
 *
 * 后端 bound_to 是内部键 `bus/RESOURCE`（总线类型小写 + 资源名，如 uart/UART0）。
 * 2026-10-02 统一：改前把它**原样**渲染在 DMA 卡上，于是同一个资源在
 * 资源表/DMA 列/资源详情里是 "UART0"，在 DMA 卡里却成了 "uart/UART0" ——
 * 大小写与写法都不一致，用户会怀疑是不是两个不同的东西。
 * 解析不出来时如实回退原值，而不是丢弃信息。
 */
export function dmaBoundToText(boundTo: string | undefined | null): string {
  const raw = String(boundTo || '').trim()
  if (!raw) return '未绑定'
  const slash = raw.indexOf('/')
  if (slash <= 0) return raw
  const bus = raw.slice(0, slash).toLowerCase()
  const resource = raw.slice(slash + 1)
  // busText 已把总线掩码映射为规范大写（UART/I2C/SPI），复用它保证口径一致。
  const label = busText(busTypeMask(bus as BusType)) || bus.toUpperCase()
  return resource + '（' + label + '）'
}

export function dmaTagClass(state: number): string {
  // 复用 DMA 状态枚举，映射到页面 .bus-tag 色系（蓝=空闲/绿=已分配/灰=已禁用）
  if (state === DmaState.ALLOCATED) return 'bus-tag-green'
  if (state === DmaState.DISABLED) return 'bus-tag-gray'
  return 'bus-tag-blue'
}

/** 能力上报里的某一类资源（id 统一转字符串，供选择态比较）。 */
export function resourcesOf(capabilities: Capabilities, busType: BusType): BusResource[] {
  const resources = capabilities?.buses?.[busType] || []
  return Array.isArray(resources) ? resources.map((resource: any) => ({ ...resource, id: String(resource.id) })) : []
}

export function resourcePins(busType: BusType, resource: BusResource): string {
  const pairs: Array<[string, unknown]> = busType === 'i2c'
    ? [['SDA', resource.default_sda_pin], ['SCL', resource.default_scl_pin]]
    : busType === 'uart'
      ? [['TX', resource.default_tx_pin], ['RX', resource.default_rx_pin]]
      : busType === 'spi'
        ? [['MOSI', resource.default_mosi_pin], ['MISO', resource.default_miso_pin], ['SCLK', resource.default_sclk_pin], ['CS', resource.default_cs_pin]]
        : busType === 'gpio' || busType === 'adc'
          ? [['GPIO', resource.pin]]
          : [['CH', resource.channel]]
  return pairs.filter(([, pin]) => pin !== undefined && pin !== null).map(([label, pin]) => `${label}${pin}`).join(' / ') || '—'
}

export function formatFrequency(value: unknown): string | null {
  const numeric = Number(value)
  if (!Number.isFinite(numeric) || numeric <= 0) return null
  if (numeric >= 1_000_000 && numeric % 1_000_000 === 0) return `${numeric / 1_000_000}MHz`
  if (numeric >= 1000 && numeric % 1000 === 0) return `${numeric / 1000}kHz`
  return `${numeric}Hz`
}

export function resourceMode(resource: BusResource): string {
  if (resource.mode === 'master') return '主机模式'
  if (resource.mode === 'slave') return '从机模式'
  if (resource.direction === 'input') return '输入'
  if (resource.direction === 'output') return '输出'
  return '—'
}

export function resourceBindingKey(busType: BusType, resource: BusResource): string {
  return `${busType}/${resource.id}`.toLowerCase()
}

export function canonicalHardwareId(type: BusType, rawId: unknown): number | null {
  const raw = String(rawId || '').trim()
  if (/^\d+$/.test(raw)) return Number(raw)
  const index = Number(raw.replace(/\D/g, ''))
  if (!Number.isFinite(index)) return null
  // 资源 ID 基数表只覆盖有基数约定的总线类型；pwm 无基数（→ undefined 时直接用索引）。
  const base = ({ i2c: 1, spi: 10, uart: 20, gpio: 30, adc: 40 } as Partial<Record<BusType, number>>)[type]
  return base === undefined ? index : base + index
}

export function hardwareResourceMatchesChannel(busType: BusType, resource: BusResource, channel: Channel): boolean {
  const resourceId = String(resource.id || '').toLowerCase()
  const channelId = String(channel.hardware_id || '').toLowerCase()
  if (resourceId === channelId) return true
  const resourceNumericId = canonicalHardwareId(busType, resource.id)
  const channelNumericId = canonicalHardwareId(busType, channel.hardware_id)
  return resourceNumericId !== null && resourceNumericId === channelNumericId
}

export function resourceMountedChannels(busType: BusType, resource: BusResource, channels: Channel[]): Channel[] {
  return channels.filter(channel => (
    String(channel.hardware_type || '').toLowerCase() === busType
    && hardwareResourceMatchesChannel(busType, resource, channel)
  ))
}

/**
 * 资源层级显示的「当前波特率」：只从该资源上已创建通道的 bus_config 解出（见 baudCurrentRate）。
 * 没有通道时返回 0，调用方如实显示"未配置"——不用 max_baud（能力上限）冒充当前值。
 * 多条通道时取第一条可解出的值：资源行是**资源视角的摘要**，
 * 逐通道的精确值在下方「已创建通道」列表里（每行一个当前值），不在此处编造聚合。
 */
export function resourceCurrentBaud(busType: BusType, resource: BusResource, channels: Channel[]): number {
  if (busType !== 'uart') return 0
  for (const ch of resourceMountedChannels(busType, resource, channels)) {
    const rate = baudCurrentRate(ch)
    if (rate !== null) return rate
  }
  return 0
}

export function resourceParameters(busType: BusType, resource: BusResource, channels: Channel[]): string[] {
  const values: string[] = []
  if (busType === 'i2c') {
    const frequency = formatFrequency(resource.freq_hz || resource.max_freq_hz)
    if (frequency) values.push(frequency)
    if (resource.mode) values.push(resource.mode === 'master' ? '主机' : '从机')
  } else if (busType === 'uart') {
    // 这里显示的是**能力上限**，不是当前波特率（2026-10-03 缺陷 4）。
    // 旧代码写 `resource.baud_rate || resource.max_baud`，而 ResourceReport 的
    // uartEntry 只有 max_baud（固件能力），没有 baud_rate 字段 —— 于是永远走
    // 回退分支，把能力上限当成当前值显示。实测现场 max_baud=5000000，
    // 资源表就显示「5000000 baud」，而通道里真实生效的是 4800，
    // 这正是用户说的"波特率参数都对不上"。
    // 当前波特率只存在通道的 bus_config 里（复用 baudCurrentRate 的读取口径），
    // 资源层级没有通道时如实显示"未配置"，绝不拿 max_baud 冒充。
    const maxBaud = Number(resource.max_baud)
    if (maxBaud > 0) values.push(`最高 ${maxBaud} baud`)
    const currentBaud = resourceCurrentBaud(busType, resource, channels)
    if (currentBaud > 0) values.push(`当前 ${currentBaud} baud`)
    else values.push('当前波特率未配置（未创建通道）')
    if (resource.data_bits) values.push(`${resource.data_bits}bit`)
  } else if (busType === 'spi') {
    const frequency = formatFrequency(resource.clock_hz || resource.max_freq_hz)
    if (frequency) values.push(frequency)
    if (resource.mode) values.push(resource.mode === 'master' ? '主机' : '从机')
  } else if (busType === 'adc') {
    if (resource.bits || resource.max_bits) values.push(`${resource.bits || resource.max_bits}bit`)
    if (resource.attenuation) values.push(String(resource.attenuation))
  } else if (busType === 'pwm') {
    if (resource.max_resolution_bits) values.push(`${resource.max_resolution_bits}bit`)
    if (resource.timer_count) values.push(`${resource.timer_count} 定时器`)
  } else if (resource.direction) {
    values.push(resource.direction === 'input' ? '输入' : '输出')
  }
  return values
}

/**
 * 该通道"绑定在哪个资源上"。
 * 资源 id 与通道 hardware_id 的对应关系与资源表**同一套口径**
 * （hardwareResourceMatchesChannel → canonicalHardwareId），因此这里反查 resources，
 * 保证「已创建通道」卡显示的"资源"与资源表「已挂载通道」列指向同一个对象，不会出现两份真相。
 *
 * 注意：这里查的是**通道自己 hardware_type** 那一类资源，而匹配仍沿用页面的当前总线上下文
 * （原实现如此），保留这一细节以免改变两处口径的比较结果。
 */
export function channelResourceLabel(busType: BusType, capabilities: Capabilities, ch: Channel): string {
  const type = String(ch.hardware_type || '').toLowerCase() as BusType
  const resources = (capabilities?.buses?.[type] || []) as any[]
  const hit = resources.find(resource => hardwareResourceMatchesChannel(busType, { ...resource, id: String(resource.id) }, ch))
  if (hit) return String(hit.id)
  // 资源未上报/不匹配时如实回落到通道自己的 hardware_id，而不是编造一个资源名。
  return String(ch.hardware_id || '').trim() || '未匹配到已上报资源'
}

// ── DMA 绑定：候选/绑定/选择器口径（资源表与资源详情共用） ──

export function dmaCandidatesOf(busType: BusType, dmaChannels: DmaChannelInfo[]): DmaChannelInfo[] {
  const mask = busTypeMask(busType)
  return mask ? dmaChannels.filter(dma => (dma.compatible_bus & mask) !== 0) : []
}

export function resourceDmaBindingOf(busType: BusType, resource: BusResource, dmaChannels: DmaChannelInfo[]): DmaChannelInfo | undefined {
  const key = resourceBindingKey(busType, resource)
  return dmaCandidatesOf(busType, dmaChannels).find(dma => String(dma.bound_to || '').toLowerCase() === key)
}

/**
 * 该 DMA 是否**已经**绑定在本资源上。
 * 与 resourceDmaBinding 的区别：后者只看候选集内（按掩码筛过），
 * 而"已绑定的那条"可能因为掩码调整/数据陈旧而不在候选集内 —— 那种情况下
 * 选择器仍要把它显示为当前值，否则用户会以为"没绑定"，一改动就等于静默解绑。
 */
export function isDmaBoundToResource(busType: BusType, dma: DmaChannelInfo, resource: BusResource): boolean {
  return String(dma.bound_to || '').toLowerCase() === resourceBindingKey(busType, resource)
}

/** 选择器当前值：'' = 不使用 DMA，否则是已绑定本资源那条的 dma_id。 */
export function dmaSelectionOf(busType: BusType, resource: BusResource, dmaChannels: DmaChannelInfo[]): string {
  const bound = dmaChannels.find(dma => isDmaBoundToResource(busType, dma, resource))
  if (bound) return String(bound.dma_id)
  const candidate = resourceDmaBindingOf(busType, resource, dmaChannels)
  return candidate ? String(candidate.dma_id) : ''
}

/** 选项文案必须带 dma_id，否则两条同名/同前缀的通道无法区分。 */
export function dmaOptionLabel(dma: DmaChannelInfo): string {
  return `${dma.name || 'DMA' + dma.dma_id}（#${dma.dma_id} · ${busText(dma.compatible_bus)}）`
}

/**
 * 资源表里已绑定值的**紧凑**文案。
 *
 * 表格的「DMA 绑定」列只有 ~138px，用完整选项文案（名字 + id + 兼容总线）
 * 必然截断成 "GDMA_CH0（#…"，反而看不出绑的是哪条。表内只需要回答
 * "绑了哪一条"，兼容总线在当前 TAB 下是冗余信息（下拉选项里仍给全）。
 */
export function dmaBoundValueLabel(dma: DmaChannelInfo | undefined): string {
  if (!dma) return '不使用 DMA'
  // 单元格仅 ~126px（可用 ~102px）：连 "GDMA_CH0（#0）" 都会截断。
  // 表内只需回答"绑了哪一条"，通道名本身在节点内已唯一；dma_id 交给
  // title 提示与下拉选项（下拉里仍带 #id 与兼容总线）。
  return dma.name || 'DMA' + dma.dma_id
}

/**
 * B3：无可用 DMA 时**必须说明原因**，不能静默留空。
 * 掩码为 0（adc/gpio/pwm 无 DMA 位）与"有掩码但设备没上报兼容通道"是两种不同成因，
 * 混成一句"暂无"会让用户把"这条总线本来就不支持 DMA"读成"功能坏了"。
 */
export function resourceDmaUnavailableText(busType: BusType, dmaChannels: DmaChannelInfo[]): string {
  if (!busTypeMask(busType)) return '该总线类型不支持 DMA'
  if (dmaChannels.length === 0) return '该节点未上报 DMA 资源'
  return '该总线暂无可兼容的 DMA 资源'
}

export function canToggleResourceDma(busType: BusType, resource: BusResource, dmaChannels: DmaChannelInfo[]): boolean {
  if (resourceDmaBindingOf(busType, resource, dmaChannels)) return true
  return dmaCandidatesOf(busType, dmaChannels).some(dma => isDmaRebindable(dma.state, dma.bound_to))
}
