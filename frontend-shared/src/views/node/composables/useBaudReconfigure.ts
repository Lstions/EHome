import { computed, ref, type Ref } from 'vue'
import { ElMessage } from 'element-plus'
import type { Capabilities } from '@/api/node'
import { channelApi, type Channel } from '@/api/channel'
import { validateBaudrate } from '@/utils/validate'
import { baudCurrentRate, channelName, isUARTChannel } from './channelDisplay'
import {
  type BusType,
  type BusResource,
  hardwareResourceMatchesChannel,
  channelResourceLabel,
} from './busResourceHelpers'

/**
 * 页签「总线配置」里**按资源批量改波特率**的弹窗目标选择与提交。
 *
 * 为什么批量弹窗留在页面层：目标集合由「当前总线子页签 + 选中资源」决定，
 * 而这两个状态属于页面（创建通道对话框也读它们）。行内单条改速率在
 * 「已创建通道」卡内部就地完成，两者措辞不同、互不复用（单条场景说"共 N 条"会误导）。
 */

interface BaudOptions {
  channels: Ref<Channel[]>
  capabilities: Ref<Capabilities>
  activeBusType: Ref<BusType>
  selectedResource: Ref<BusResource | null>
  nodeOffline: Ref<boolean>
  nodeSerial: Ref<string>
  /** 提交成功后必须刷新通道列表，否则卡片还显示旧波特率。 */
  fetchChannels: () => Promise<void>
}

export function useBaudReconfigure(options: BaudOptions) {
  // 目标选择两档（见下方 baudToolSelection）：
  //   1. 用户在「已创建通道」行里点了「改波特率」⇒ 只改该通道；
  //   2. 否则用上方资源表选中的 UART 资源 ⇒ 批量改该资源上的全部 UART 通道。
  // 两档都不成立时入口**禁用**并说明怎么选 —— 既不让用户白点一下再吃 400，
  // 也不伪造一个「默认通道」当目标。
  const baudToolVisible = ref(false)
  const baudPickedChannel = ref<Channel | null>(null)
  const baudTarget = ref(9600)
  const baudSubmitting = ref(false)
  /** 失败原因就地展示在对话框里（活动元素被禁用/卸载时 ElMessage 可能不可达）。 */
  const baudToolError = ref('')

  function closeBaudTool() {
    baudToolVisible.value = false
    baudToolError.value = ''
    baudSubmitting.value = false
  }

  /** 弹窗入口（按资源批量改）。保留原语义，仅重命名以区别于内联入口。 */
  function openBaudToolInline() {
    baudPickedChannel.value = null
    openBaudTool()
  }

  /** 上方资源表里当前选中的资源（仅 UART 有意义）。 */
  const baudSelectedResource = computed<BusResource | null>(() => (
    options.activeBusType.value === 'uart' ? options.selectedResource.value : null
  ))

  /** 本次是否有明确目标（选中通道 或 选中 UART 资源）。布尔，避免 'none' 这类真值字符串。 */
  const baudToolSelection = computed(() => Boolean(baudPickedChannel.value || baudSelectedResource.value))

  /** 该资源上的 UART 通道（口径与资源表「已挂载通道」列一致）。 */
  const baudScopeChannels = computed<Channel[]>(() => {
    if (baudPickedChannel.value) return [baudPickedChannel.value]
    const resource = baudSelectedResource.value
    if (!resource) return []
    return options.channels.value.filter(ch => (
      isUARTChannel(ch)
      && hardwareResourceMatchesChannel(options.activeBusType.value, { ...resource, id: String(resource.id) }, ch)
    ))
  })
  const baudScopeCount = computed(() => baudScopeChannels.value.length)

  /**
   * 入口可用性：节点离线或没有目标时**禁用**按钮，并说明原因，而不是让用户提交后吃 400。
   * 这里只做前端可知的判断（节点状态 + 选中通道/资源）；通道归属仍由后端独立校验
   * —— 前端不重复实现它，两套规则迟早分叉。
   */
  const baudToolAvailable = computed(() => !options.nodeOffline.value && baudToolSelection.value)
  const baudToolSelectionText = computed(() => {
    if (baudPickedChannel.value) return '已选通道：' + channelName(baudPickedChannel.value)
    if (baudSelectedResource.value) return '已选资源：' + baudSelectedResource.value.id + '（' + baudScopeCount.value + ' 条 UART 通道）'
    return '未选中通道：请先在上方资源表选一条 UART 资源，或在通道行点「改波特率」'
  })

  const baudToolTargetText = computed(() => {
    const picked = baudPickedChannel.value
    if (!picked) return ''
    const current = baudCurrentRate(picked)
    return '目标通道「' + channelName(picked) + '」（资源 ' + channelResourceLabel(options.activeBusType.value, options.capabilities.value, picked) + '）'
      + (current === null ? '' : '，当前 ' + current + ' bit/s')
  })

  /** 能力驱动上限；无能力数据时回落到与「创建通道」兜底一致的 921600。 */
  const baudMax = computed(() => {
    const picked = baudPickedChannel.value
    const capsMax = picked ? resourceCapsFor(picked)?.baud_rate_max : null
    const resourceMax = (baudSelectedResource.value as any)?.max_baud
    const raw = Number(capsMax ?? resourceMax)
    return Number.isFinite(raw) && raw > 0 ? raw : 921600
  })

  /** 从能力数据里取该通道所属资源的能力项（找不到返回 null，不做推测）。 */
  function resourceCapsFor(ch: Channel): any | null {
    const resources = (options.capabilities.value?.buses?.uart || []) as any[]
    return resources.find(r => hardwareResourceMatchesChannel(options.activeBusType.value, { ...r, id: String(r.id) }, ch)) || null
  }

  function openBaudTool() {
    const targets = baudScopeChannels.value
    if (targets.length) {
      const first = baudCurrentRate(targets[0])
      const allSame = targets.every(ch => baudCurrentRate(ch) === first)
      // 预填「当前值」而不是某个常量：常量预填会让用户一确认就把一批通道改成 9600。
      baudTarget.value = allSame && first !== null ? first : 9600
    }
    baudToolError.value = ''
    baudToolVisible.value = true
  }

  /** 提交：逐个通道调用真实重配置端点，逐条如实汇报成功/未改动/失败。 */
  async function confirmBaudReconfigure() {
    if (baudSubmitting.value) return
    const baud = Number(baudTarget.value)
    if (!validateBaudrate(baud)) {
      baudToolError.value = '请选择标准波特率（300/600/1200/2400/4800/9600/19200/38400/57600/115200）'
      return
    }
    const targets = baudScopeChannels.value
    if (!targets.length) {
      baudToolError.value = '没有可修改的 UART 通道：请先在资源表选一条 UART 资源，或在通道行点「改波特率」'
      return
    }
    baudSubmitting.value = true
    baudToolError.value = ''
    const serial = options.nodeSerial.value
    let changed = 0
    let unchanged = 0
    const failures: string[] = []
    for (const ch of targets) {
      try {
        const res = await channelApi.reconfigure(Number(ch.id), baud)
        if (res?.status === 'unchanged') unchanged += 1
        else changed += 1
      } catch (err: any) {
        failures.push(channelName(ch) + '：' + (err?.message || '下发失败'))
      }
    }
    // 提交期间节点可能已切换/组件已卸载 ⇒ 不再改 UI、也不再提示（避免切页后弹上一次的结果）。
    if (serial !== options.nodeSerial.value) return
    baudSubmitting.value = false
    if (failures.length) {
      baudToolError.value = failures.join('；')
      if (changed || unchanged) {
        ElMessage.warning('部分通道已提交：成功 ' + changed + ' 条、未改动 ' + unchanged + ' 条、失败 ' + failures.length + ' 条')
        await options.fetchChannels()
      }
      return
    }
    closeBaudTool()
    if (changed === 0) ElMessage.info('目标波特率与当前一致，未做改动')
    else if (unchanged === 0) ElMessage.success('已提交波特率修改并下发（' + changed + ' 条通道）')
    else ElMessage.success('已提交波特率修改：' + changed + ' 条已下发，' + unchanged + ' 条与当前一致')
    await options.fetchChannels()
  }

  /** 卸载：选中态只在「用户刚点了某一行」的上下文里有意义，不能留给下次挂载。 */
  function resetForUnmount() {
    baudPickedChannel.value = null
    baudToolVisible.value = false
    baudToolError.value = ''
    baudSubmitting.value = false
  }

  return {
    baudToolVisible,
    baudTarget,
    baudSubmitting,
    baudToolError,
    baudToolSelection,
    baudToolAvailable,
    baudToolSelectionText,
    baudToolTargetText,
    baudMax,
    openBaudToolInline,
    confirmBaudReconfigure,
    resetForUnmount,
  }
}
