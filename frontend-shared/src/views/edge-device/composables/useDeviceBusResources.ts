/**
 * 向导「硬件ID」下拉的资源真源：节点能力上报（GET /nodes/:id/capabilities）→ 总线资源。
 *
 * 边界：只做「取一次能力 + 由能力派生可选项/提示/步骤放行」，
 * 不持有表单本身（deviceForm / newChannel 由创建组合式创建后作为依赖传进来，读的是同一批对象）。
 *
 * 为什么绝不硬编码资源名：通道的 hardware_id 是**总线资源标识**（UART0 / SPI2），
 * 设备实际只有它上报的那几条；硬编码会造出指向不存在总线的通道（静默的错误数据）。
 */
import { computed, ref } from 'vue'
import { useChannelStore } from '@/stores/channel'
import { nodeApi } from '@/api/node'
import { logger } from '@/utils/logger'
import { getHardwareLabel } from '@/utils/hardwareTag'

/** 本模块只需要读到表单的这两个字段（传 reactive 对象本身，保持响应式）。 */
export interface DeviceBusResourceDeps {
  deviceForm: { node_id: number | null }
  newChannel: { hardware_type: string }
}

export function useDeviceBusResources(deps: DeviceBusResourceDeps) {
  const { deviceForm, newChannel } = deps
  const channelStore = useChannelStore()

  /**
   * 向导用的节点能力缓存（node_id → buses）。
   *
   * 「硬件ID」下拉必须来自**设备真实上报**的总线资源（见下方 availableBusesForType），
   * 因此打开向导/切换节点时按需拉取一次 GET /nodes/:id/capabilities。
   * 拉取失败时**不伪造资源**：下拉退回「该节点已有通道的 hardware_id」，仍无则显示未上报提示。
   */
  const nodeCapabilitiesByNode = ref<Record<string, Record<string, any>>>({})
  const capabilitiesLoading = ref(false)
  const capabilitiesRequestSequence = ref(0)
  const loadNodeCapabilities = async (nodeId: string | number | null | undefined) => {
    const key = String(nodeId ?? '')
    if (!key || nodeCapabilitiesByNode.value[key] || capabilitiesLoading.value) return
    const sequence = ++capabilitiesRequestSequence.value
    capabilitiesLoading.value = true
    try {
      const caps = await nodeApi.getCapabilities(key)
      if (sequence !== capabilitiesRequestSequence.value) return
      const buses = (caps as any)?.buses
      nodeCapabilitiesByNode.value = {
        ...nodeCapabilitiesByNode.value,
        [key]: buses && typeof buses === 'object' ? buses : {},
      }
    } catch (error) {
      // 能力上报读不到不是致命错误：下拉会退化为该节点已有通道，绝不伪造资源名。
      logger.warn('获取节点能力失败，硬件资源下拉回退为已有通道', { error: String(error) })
    } finally {
      if (sequence === capabilitiesRequestSequence.value) capabilitiesLoading.value = false
    }
  }

  /**
   * 该节点在**能力上报**里声明的总线资源 id（唯一真源）。
   *
   * 为什么必须读能力上报而不是硬编码：通道的 hardware_id 是**总线资源标识**
   * （如 UART0 / SPI2），设备实际只有它上报的那几条。改前这里在
   * 「该节点还没有此类通道」时返回硬编码的 ['UART1','UART2'] / ['I2C0','I2C1'] /
   * ['SPI1','SPI2']，既不读 capabilities、又是 1 基编号 —— 实测本机节点上报的是
   * uart=[UART0,UART1]、i2c=[I2C0]、spi=[SPI2]，于是下拉里出现设备上根本不存在的
   * UART2 / I2C1 / SPI1；用户一旦选中就会创建一个指向不存在总线的通道（静默的错误数据）。
   */
  const busResourcesForType = (hardwareType: string): string[] => {
    const nodeKey = String(deviceForm.node_id ?? '')
    const buses = nodeCapabilitiesByNode.value[nodeKey]
    if (!buses) return []
    const list = (buses as Record<string, unknown>)[hardwareType.toLowerCase()]
    if (!Array.isArray(list)) return []
    return list
      .map((resource: any) => String(resource?.id ?? '').trim())
      .filter(Boolean)
  }

  /** 该节点是否**上报过**这个总线类型（用于区分「未上报」与「上报了 0 条」）。 */
  const busTypeReported = (hardwareType: string): boolean => {
    const nodeKey = String(deviceForm.node_id ?? '')
    const buses = nodeCapabilitiesByNode.value[nodeKey]
    if (!buses) return false
    return Array.isArray((buses as Record<string, unknown>)[hardwareType.toLowerCase()])
  }

  /** 「硬件ID」下拉是否有可选项（无 ⇒ 必须提示未上报并禁用「下一步」）。 */
  const hasBusOptions = computed(() => availableBusesForType(newChannel.hardware_type).length > 0)
  /** 未上报提示文案（下拉空态与步骤校验共用，措辞只有一处）。 */
  const unavailableBusMessage = computed(() =>
    `该节点未上报 ${getHardwareLabel(newChannel.hardware_type || 'BUS')} 资源，无法在此创建该总线的通道`)

  /**
   * 向导「硬件ID」下拉的选项：通道 hardware_id 是**总线资源标识**语义，取值优先级：
   *   1) 该节点能力上报的资源 id（与设备一致，0 基）
   *   2) 该节点已有通道的 hardware_id（兼容能力上报缺失的老后端，但至少是真实存在的）
   * 两者都拿不到 ⇒ 空数组，由 UI 明确提示「未上报」并禁用下一步。
   * **绝不**回退到硬编码资源名 —— 那是把不存在的资源伪装成可选。
   */
  const availableBusesForType = (hardwareType: string): string[] => {
    if (!deviceForm.node_id) return []
    const fromCapabilities = busResourcesForType(hardwareType)
    if (fromCapabilities.length > 0) return fromCapabilities
    // P0-2: node_id / hardware_type 比较归一化——channel.node_id 可能是
    // string(设备序列号如 'F0F5BDFFFE02')也可能是 number;channel.hardware_type
    // 后端存大写 'UART',表单是小写 'uart'。两者都需宽松匹配,否则 filter 空导致
    // 硬件ID下拉无选项。
    const nodeIdStr = String(deviceForm.node_id)
    const hwTypeLower = hardwareType.toLowerCase()
    const collectorChannels = channelStore.channels.filter(
      ch => ch && String(ch.node_id) === nodeIdStr && (ch.hardware_type || '').toLowerCase() === hwTypeLower
    )
    // 提取已有的 hardware_id（如 I2C0, UART0），去重
    return [...new Set(collectorChannels.map(ch => ch.hardware_id))].filter(Boolean) as string[]
  }


  return {
    nodeCapabilitiesByNode, capabilitiesLoading, loadNodeCapabilities,
    busResourcesForType, busTypeReported, hasBusOptions, unavailableBusMessage, availableBusesForType,
  }
}
