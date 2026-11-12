import { computed, ref, watch, type Ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import type { Capabilities, DmaChannelInfo } from '@/api/node'
import type { Channel } from '@/api/channel'
import { useDmaStore } from '@/stores/dma'
import { isDmaRebindable } from '@/utils/dmaState'
import { nodeApi } from '@/api/node'
import { getSessionGeneration, assertSessionGeneration } from '@/utils/sessionCache'
import feedback from '@/utils/feedback'
import {
  type BusType,
  type BusResource,
  resourcesOf,
  resourceBindingKey,
  resourceDmaBindingOf,
  dmaCandidatesOf,
  isDmaBoundToResource,
} from './busResourceHelpers'

/**
 * 总线配置页签的**选择态与写动作**（选了哪条总线/哪条资源、DMA 绑定、I2C 地址扫描）。
 *
 * 为什么这部分留在页面层而不是塞进总线配置子组件：其中三样东西在页签之外仍被使用 ——
 * 创建通道对话框的 preset（当前总线类型 + 选中资源）、「DMA 通道」页签的刷新触发、
 * 以及路由切换时的统一重置。子组件只拿到"当前选中是谁"的只读快照，
 * 写动作回到页面这一处，避免同一个选择态出现两份可写副本。
 *
 * 派生展示（引脚/参数/挂载通道/DMA 文案）不在这里：它们由 busResourceHelpers 提供，
 * 由渲染方按自己的上下文调用，避免为了展示再把状态搬来搬去。
 */

interface BusConfigOptions {
  capabilities: Ref<Capabilities>
  channels: Ref<Channel[]>
  nodeSerial: Ref<string>
  nodeOffline: Ref<boolean>
  /** 页面级操作代际（路由切换/卸载时自增，使在途写操作结果失效）。 */
  opGeneration: Ref<number>
}

export function useBusConfig(options: BusConfigOptions) {
  const route = useRoute()
  const dmaStore = useDmaStore()

  const activeBusType = ref<BusType>('i2c')
  const selectedResourceId = ref('')
  const busPage = ref(1)
  const busPageSize = 10
  const i2cScanning = ref(false)
  const scanResult = ref<string[] | null>(null)

  const activeBusResources = computed<BusResource[]>(() => resourcesOf(options.capabilities.value, activeBusType.value))
  const selectedResource = computed(() => activeBusResources.value.find(resource => resource.id === selectedResourceId.value) || null)
  const busTotalPages = computed(() => Math.max(1, Math.ceil(activeBusResources.value.length / busPageSize)))
    // GPIO/PWM 是直接控制资源，不能错误地纳入协议通道系统。
  const busSupportsChannels = computed(() => ['uart', 'i2c', 'spi', 'adc'].includes(activeBusType.value))

  /**
   * 能力数据落地后的选中态钳制。
   *
   * 原先这两行写在 fetchBusData 的 try 分支里（能力赋值之后、busLoading 落下之前）。
   * 抽出来由一个显式回调调用，是为了让"取数"与"选择态归位"各自只在一处，
   * 且新数据里原选中资源不存在时（设备重新上报过）必须立刻归位到第一条。
   */
  function clampSelectionAfterCapabilities() {
    const resources = activeBusResources.value
    selectedResourceId.value = resources.some(resource => resource.id === selectedResourceId.value)
      ? selectedResourceId.value
      : (resources[0]?.id || '')
    busPage.value = Math.min(busPage.value, busTotalPages.value)
  }

  function selectBusType(type: BusType) {
    activeBusType.value = type
    busPage.value = 1
    scanResult.value = null
    selectedResourceId.value = activeBusResources.value[0]?.id || ''
  }

  // D4：三个选中入口（整行点击、资源名按钮、「查看」按钮）都只写 selectedResourceId，
  // 右侧「资源详情」由 selectedResource 统一派生（资源名称/引脚/关键参数/已挂载通道/DMA 绑定）。
  // 清掉只属于上一行的瞬态（I2C 地址扫描结果）放在这里做一次，避免三个入口各写一份、
  // 漏掉任意一个就会把上一行的扫描结果挂到新资源上（张冠李戴）。
  watch(selectedResourceId, () => { scanResult.value = null })

  /** B1：选择器选中后绑定/解绑。复用 dmaStore.toggle（含并发守卫与"必须指定 bindTo"防御）。 */
  async function changeResourceDma(resource: BusResource, value: string) {
    if (options.nodeOffline.value) return
    const all = dmaStore.mergedChannels as DmaChannelInfo[]
    const target = value ? all.find(dma => String(dma.dma_id) === value) : undefined
    if (value && !target) {
      ElMessage.warning('所选 DMA 资源已不存在，请刷新后重试')
      return
    }
    if (!target) {
      // 选择「不使用 DMA」= 解绑当前绑定在本资源上的那条
      const bound = all.find(dma => isDmaBoundToResource(activeBusType.value, dma, resource))
      if (!bound) return
      await toggleResourceDma(resource, false)
      return
    }
    // 同一资源上只能有一条 DMA：先把旧的解绑，否则会留下两条都指向本资源
    const previous = all.find(dma => isDmaBoundToResource(activeBusType.value, dma, resource) && dma.dma_id !== target.dma_id)
    if (previous) await toggleResourceDma(resource, false)
    await toggleResourceDma(resource, true, target)
  }

  async function toggleResourceDma(resource: BusResource, enabled: boolean | string | number, explicit?: DmaChannelInfo) {
    if (options.nodeOffline.value) return
    const id = route.params.id as string
    const serial = options.nodeSerial.value
    const desired = Boolean(enabled)
    const bound = resourceDmaBindingOf(activeBusType.value, resource, dmaStore.mergedChannels as DmaChannelInfo[])
    // B1：显式指定的目标优先（用户在选择器里选的那条），
    // 否则退回"已绑定那条 / 第一条可重绑定的候选"——即改前开关的隐式行为，保持向后兼容。
    const candidate = explicit || bound || dmaCandidatesOf(activeBusType.value, dmaStore.mergedChannels as DmaChannelInfo[])
      .find(dma => isDmaRebindable(dma.state, dma.bound_to))
    if (!candidate) {
      ElMessage.warning('没有可用于此资源的 DMA 通道')
      return
    }
    const operation = options.opGeneration.value
    const sessionGeneration = getSessionGeneration()
    try {
      await dmaStore.toggle(serial, candidate, desired, desired ? resourceBindingKey(activeBusType.value, resource) : '')
      assertSessionGeneration(sessionGeneration)
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      ElMessage.success(desired ? 'DMA 已绑定' : 'DMA 已解绑')
    } catch (err: any) {
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      feedback.handleError(err, 'DMA 配置保存失败')
    }
  }

  async function scanSelectedI2C() {
    if (options.nodeOffline.value || activeBusType.value !== 'i2c' || !selectedResource.value) return
    const id = route.params.id as string
    const serial = options.nodeSerial.value
    const operation = options.opGeneration.value
    const sessionGeneration = getSessionGeneration()
    i2cScanning.value = true
    scanResult.value = null
    try {
      const result = await nodeApi.scanI2C(serial, selectedResource.value.id)
      assertSessionGeneration(sessionGeneration)
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      scanResult.value = Array.isArray(result?.devices) ? result.devices : []
      if (scanResult.value.length) ElMessage.success(`发现 ${scanResult.value.length} 个设备`)
      else ElMessage.info('设备未返回可发现的 I2C 地址')
    } catch (err: any) {
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      feedback.handleError(err, '地址扫描失败')
    } finally {
      if (operation === options.opGeneration.value && route.params.id === id) i2cScanning.value = false
    }
  }

  /** 路由切换：选中资源与扫描结果属于「上一节点的上下文」，必须清空。 */
  function resetForRouteChange() {
    selectedResourceId.value = ''
    scanResult.value = null
  }

  return {
    activeBusType,
    selectedResourceId,
    busPage,
    busPageSize,
    i2cScanning,
    scanResult,
    activeBusResources,
    selectedResource,
    busTotalPages,
    busSupportsChannels,
    selectBusType,
    clampSelectionAfterCapabilities,
    changeResourceDma,
    toggleResourceDma,
    scanSelectedI2C,
    resetForRouteChange,
  }
}

export type { BusType, BusResource }
