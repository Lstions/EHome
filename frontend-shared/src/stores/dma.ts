import { defineStore } from 'pinia'
import { nodeApi, type DmaChannelInfo, type DmaChannelConfig } from '@/api/node'
import { DmaState } from '@/utils/dmaState'

/**
 * DMA 通道统一状态管理 (v2.5)
 *
 * 替代 NodeDetail.vue 和 ChannelPanel.vue 中各自维护的独立状态，
 * 两个组件共享同一份 DMA 数据，开关操作通过 store 统一处理。
 *
 * 核心规则：
 * - 开关状态 = bound_to 非空（表示已绑定到某个硬件）
 * - toggle 操作通过 store.toggleDma() 统一执行
 * - 乐观更新 + API 失败回滚
 */
export const useDmaStore = defineStore('dma', () => {
  // ========== State ==========
  const channels = ref<DmaChannelInfo[]>([])
  const loading = ref(false)
  const toggling = ref<Record<number, boolean>>({})  // dma_id → loading

  // 乐观更新覆盖层：dma_id → target state (0=free, 2=disabled)
  const overrideState = ref<Record<number, number>>({})

  // bind_to 缓存：关闭时记住，重新打开时恢复
  const bindToCache = ref<Record<number, string>>({})

  // ========== Getters ==========

  /** 开关是否开启（统一判断：bound_to 非空 = ON） */
  const isSwitchOn = computed(() => (dma: DmaChannelInfo): boolean => {
    const ov = overrideState.value[dma.dma_id]
    if (ov !== undefined) return ov !== 2
    return !!dma.bound_to
  })

  /** 获取 DMA 通道列表（合并 override 后的状态） */
  const mergedChannels = computed(() => {
    return channels.value.map(dma => {
      const ov = overrideState.value[dma.dma_id]
      if (ov === undefined) return dma
      return { ...dma, state: ov, bound_to: ov === 2 ? '' : (bindToCache.value[dma.dma_id] || dma.bound_to) }
    })
  })

  // ========== Actions ==========

  async function fetch(collectorId: string | number) {
    loading.value = true
    try {
      const list = await nodeApi.getDmaChannels(collectorId)
      channels.value = list
      overrideState.value = {}
      bindToCache.value = {}
    } finally {
      loading.value = false
    }
  }

  async function toggle(collectorId: string | number, dma: DmaChannelInfo, enabled: boolean, bindTo?: string) {
    const dmaId = dma.dma_id

    // 并发守卫：防止快速点击触发重复请求
    if (toggling.value[dmaId]) return

    // 防御检查：启用 DMA 必须指定绑定目标，否则会产生孤儿状态
    if (enabled && !bindTo && !bindToCache.value[dmaId] && !dma.bound_to) {
      throw new Error('启用 DMA 必须指定绑定目标（bindTo）')
    }

    // 乐观更新
    overrideState.value[dmaId] = enabled ? DmaState.ALLOCATED : DmaState.DISABLED
    if (enabled && bindTo) {
      bindToCache.value[dmaId] = bindTo
    } else if (!enabled && dma.bound_to) {
      bindToCache.value[dmaId] = dma.bound_to
    }
    toggling.value[dmaId] = true

    try {
      // v2.5: 只发送需要更新的通道，后端会合并保留其他通道的原状态
      const configs: DmaChannelConfig[] = []
      
      // 当前操作的通道
      configs.push({
        dma_id: dmaId,
        enabled,
        bind_to: enabled ? (bindTo || bindToCache.value[dmaId] || dma.bound_to || '') : ''
      })
      
      // 如果启用此通道并绑定到某硬件，解绑其他已绑定同一硬件的通道（互斥）
      // 使用 mergedChannels 确保乐观更新期间的绑定状态也被检测到
      if (enabled && bindTo) {
        const hwKey = bindTo.toLowerCase()
        for (const ch of mergedChannels.value) {
          if (ch.dma_id === dmaId) continue
          if (ch.bound_to && ch.bound_to.toLowerCase() === hwKey) {
            configs.push({ dma_id: ch.dma_id, enabled: false, bind_to: '' })
          }
        }
      }
      
      await nodeApi.updateDmaConfig(collectorId, configs)

      // 刷新真实数据
      await fetch(collectorId)
    } catch (e: any) {
      // 回滚乐观更新
      delete overrideState.value[dmaId]
      // 清理残留的 bindToCache，防止脏数据
      delete bindToCache.value[dmaId]
      throw e
    } finally {
      toggling.value[dmaId] = false
    }
  }

  return {
    channels,
    mergedChannels,
    loading,
    toggling,
    overrideState,
    bindToCache,
    isSwitchOn,
    fetch,
    toggle
  }
})
