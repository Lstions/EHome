/**
 * 向导的「型号 / 通道」选择与步骤校验（纯派生，无自身状态）。
 *
 * 搬出来的理由：这一段全是**由表单状态派生的可选项与放行条件**
 * （解析器列表、通道卡片列表、自动通道名、下一步是否允许），
 * 与「提交事务」和「数据加载」是两件事；放在一起会把 959 行的组合式撑成上帝对象。
 *
 * 依赖全部由创建组合式传入（同一批 ref），本模块不 new 任何状态。
 */
import { computed, type ComputedRef, type Ref } from 'vue'
import { useChannelStore } from '@/stores/channel'
import { useParserStore } from '@/stores/parser'
import type { Channel } from '@/api/channel'
import type { Parser } from '@/api/parser'

export interface CreateWizardSelectionDeps {
  deviceForm: { node_id: number | null; name: string; hardware_type: string }
  newChannel: { hardware_type: string; hardware_id: string; address: string; spi_cs: number }
  selectedParser: Ref<Parser | null>
  selectedChannel: Ref<Channel | null>
  createStep: Ref<number>
  channelTab: Ref<string>
  hasBusOptions: ComputedRef<boolean>
  commandIntervalsError: Ref<string>
  prepareCommandIntervals: () => void
}

export function useCreateWizardSelection(deps: CreateWizardSelectionDeps) {
  const {
    deviceForm, newChannel, selectedParser, selectedChannel,
    createStep, channelTab, hasBusOptions, commandIntervalsError, prepareCommandIntervals,
  } = deps
  const channelStore = useChannelStore()
  const parserStore = useParserStore()

  // 解析器可选列表
  const availableParsers = computed(() => {
    return parserStore.parsers.filter(p => p.hardware_types?.length > 0)
  })

  // 根据已选解析器，筛选可用的总线类型
  const selectedParserBusTypes = computed(() => {
    return selectedParser.value?.hardware_types || ['i2c', 'spi', 'uart']
  })

  // 已有通道列表（按节点和解析器总线类型过滤）
  const existingChannels = computed(() => {
    if (!selectedParser.value) return []
    const nodeIdStr = String(deviceForm.node_id ?? '')
    const parserBuses = selectedParser.value!.hardware_types.map(b => b.toLowerCase())
    return channelStore.channels.filter(ch =>
      ch && String(ch.node_id) === nodeIdStr &&
      parserBuses.includes((ch.hardware_type || '').toLowerCase())
    )
  })

  // 自动生成的通道名称（预览用）
  const generatedChannelName = computed(() => {
    if (newChannel.hardware_type === 'i2c' && newChannel.address) {
      return `${newChannel.hardware_id}_${newChannel.address}`
    }
    if (newChannel.hardware_type === 'spi') {
      return `${newChannel.hardware_id}_CS${newChannel.spi_cs}`
    }
    return newChannel.hardware_id
  })

  // 是否可以进入下一步
  const canGoNext = computed(() => {
    // 三步向导（2026-10-23 逻辑设备移除后原「历史数据继承」第 0 步删除）：
    //   0 选择设备型号 → 1 选择节点&通道 → 2 基本信息
    if (createStep.value === 0) return !!selectedParser.value
    if (createStep.value === 1) {
      if (!deviceForm.node_id) return false
      if (channelTab.value === 'existing') return !!selectedChannel.value
      // D6(b)：该节点未上报所选总线资源时，不允许用硬编码资源名继续创建
      // （否则会造出指向不存在总线的通道）。hasBusOptions 为假时下拉本身已禁用，
      // 这里再把「下一步」一并挡住，避免用户以为能跳过。
      if (channelTab.value === 'create') return !!newChannel.hardware_id && hasBusOptions.value
    }
    if (createStep.value === 2) return !!deviceForm.name
    return false
  })

  const onNewChannelBusTypeChange = () => {
    // 切换总线类型后，上一条资源 id 对新类型没有意义（UART0 不是 SPI 资源）。
    newChannel.hardware_id = ''
  }

  const selectParser = (parser: Parser) => {
    selectedParser.value = parser
    selectedChannel.value = null
    // EDGE-WIZ-004/005: 切换设备型号时清理旧型号的逐指令轮询间隔。
    // CreateWizardCommandIntervals 监听 deviceType 自动重置已加载指令;
    // 此处同步清空提交时使用的快照, 保证重选型号不带旧 intervals。
    commandIntervalsError.value = ''
    prepareCommandIntervals()
    // P0-1: 同步新通道硬件类型到解析器第一个可用总线,避免默认值 i2c 与
    // 解析器 hardware_types 不匹配(如选 uart 解析器但表单默认 i2c)。
    const buses = parser.hardware_types || []
    if (buses.length > 0 && !buses.includes(newChannel.hardware_type)) {
      newChannel.hardware_type = buses[0]
      newChannel.hardware_id = ''
    }
  }

  // ---- EDGE-WIZ-004/005: 创建向导逐指令轮询间隔 ----
  // 由 CreateWizardCommandIntervals 子组件 (仅创建模式) 加载驱动 schedulable

  const selectChannel = (ch: Channel) => {
    selectedChannel.value = ch
  }

  return {
    availableParsers, selectedParserBusTypes, existingChannels, generatedChannelName, canGoNext,
    onNewChannelBusTypeChange, selectParser, selectChannel,
  }
}
