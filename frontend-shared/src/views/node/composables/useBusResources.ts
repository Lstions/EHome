import { ref, type Ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { nodeApi, type Capabilities } from '@/api/node'
import { useDmaStore } from '@/stores/dma'
import { getSessionGeneration, assertSessionGeneration } from '@/utils/sessionCache'
import { logger } from '@/utils/logger'
import feedback from '@/utils/feedback'

/**
 * 总线能力数据（ResourceReport 的只读映射）与「查询资源」动作。
 *
 * 为什么能力数据留在页面层、而不放进总线配置子组件：它同时也是
 * 「DMA 通道」空态的判据（是否上报了 buses.dma）与创建通道对话框的入参，
 * 而这两个使用点都在总线页签之外。放进子组件会让它只能在页签打开时才可信。
 *
 * 这个文件只管「取数与刷新动作」；资源/通道/DMA 的派生展示全部在
 * busResourceHelpers 与总线配置组件里，避免同一份能力数据出现第二套解释。
 */

interface BusResourceOptions {
  nodeSerial: Ref<string>
  nodeOffline: Ref<boolean>
  /** 页面级操作代际（路由切换/卸载时自增，使在途写操作结果失效）。 */
  opGeneration: Ref<number>
  /** 能力数据落地后由总线配置视图收回选中态（选中项可能已不存在）。 */
  onCapabilitiesLoaded: () => void
}

export function useBusResources(options: BusResourceOptions) {
  const route = useRoute()
  const dmaStore = useDmaStore()

  const capabilities = ref<Capabilities>({ buses: {} })
  const busLoading = ref(false)
  const busLoadError = ref(false)
  const busDataLoaded = ref(false)
  const resourceQuerying = ref(false)
  /** 能力数据的独立代际：与节点详情/通道互不影响（三者的返回顺序不可假设）。 */
  let capabilitiesSequence = 0

  async function fetchBusData() {
    const id = route.params.id as string
    const serial = options.nodeSerial.value
    if (!id || !serial) return
    const sequence = ++capabilitiesSequence
    busLoading.value = true
    busLoadError.value = false
    try {
      const reportedCapabilities = await nodeApi.getCapabilities(serial)
      if (sequence !== capabilitiesSequence || route.params.id !== id) return
      capabilities.value = reportedCapabilities || { buses: {} }
      busDataLoaded.value = true
      // 选中态与分页钳制紧跟在能力落地之后：新数据里原选中资源可能已不存在，
      // 不钳制就会让右侧详情显示一条已经不在表里的资源。
      options.onCapabilitiesLoaded()
      // DMA 是增强信息；失败不可伪装成总线资源读取失败。
      void dmaStore.fetch(serial).catch(err => logger.warn('获取 DMA 通道失败', { error: String(err) }))
    } catch (err: any) {
      if (sequence !== capabilitiesSequence || route.params.id !== id) return
      capabilities.value = { buses: {} }
      busLoadError.value = true
      logger.error('获取节点总线能力失败', { error: String(err) })
    } finally {
      if (sequence === capabilitiesSequence) busLoading.value = false
    }
  }

  async function requestBusResourceRefresh() {
    if (options.nodeOffline.value || resourceQuerying.value) return
    const id = route.params.id as string
    const serial = options.nodeSerial.value
    const operation = options.opGeneration.value
    const sessionGeneration = getSessionGeneration()
    resourceQuerying.value = true
    try {
      await nodeApi.queryResources(serial)
      assertSessionGeneration(sessionGeneration)
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      // ResourceReport 异步回写；等待与 ChannelPanel 相同的上报窗口，避免旧缓存冒充新值。
      await new Promise<void>(resolve => setTimeout(resolve, 2000))
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      await fetchBusData()
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      ElMessage.success('已读取设备最新资源上报')
    } catch (err: any) {
      if (operation !== options.opGeneration.value || route.params.id !== id) return
      feedback.handleError(err, '资源刷新请求失败')
    } finally {
      if (operation === options.opGeneration.value && route.params.id === id) resourceQuerying.value = false
    }
  }

  function resetForRouteChange() {
    capabilitiesSequence++
    capabilities.value = { buses: {} }
    busDataLoaded.value = false
  }

  function stopAll() {
    capabilitiesSequence++
  }

  return {
    capabilities,
    busLoading,
    busLoadError,
    busDataLoaded,
    resourceQuerying,
    fetchBusData,
    requestBusResourceRefresh,
    resetForRouteChange,
    stopAll,
  }
}
