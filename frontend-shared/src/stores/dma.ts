import { defineStore } from 'pinia'
import { nodeApi, type DmaChannelInfo, type DmaChannelConfig } from '@/api/node'

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
    } finally {
      loading.value = false
    }
  }

  async function toggle(collectorId: string | number, dma: DmaChannelInfo, enabled: boolean, bindTo?: string) {
    const dmaId = dma.dma_id

    // 乐观更新
    overrideState.value[dmaId] = enabled ? 1 : 2   // 1=allocated, 2=disabled
    if (enabled && bindTo) {
      bindToCache.value[dmaId] = bindTo
    } else if (!enabled && dma.bound_to) {
      bindToCache.value[dmaId] = dma.bound_to
    }
    toggling.value[dmaId] = true

    try {
      // v2.5: 发送所有 DMA 通道的完整配置，确保互斥。
      // 如果启用此 DMA 并绑定到某硬件，先解绑其他已绑定同一硬件的 DMA。
      const allConfigs: DmaChannelConfig[] = []
      const hwKey = (enabled && bindTo) ? bindTo.toLowerCase() : ''
      for (const ch of channels.value) {
        if (ch.dma_id === dmaId) {
          allConfigs.push({
            dma_id: dmaId,
            enabled,
            bind_to: enabled ? (bindTo || bindToCache.value[dmaId] || ch.bound_to || '') : ''
          })
        } else if (hwKey && ch.bound_to && ch.bound_to.toLowerCase() === hwKey) {
          // 解绑已占用同一硬件的其他 DMA
          allConfigs.push({ dma_id: ch.dma_id, enabled: false, bind_to: '' })
        } else {
          allConfigs.push({ dma_id: ch.dma_id, enabled: ch.state !== 2, bind_to: ch.bound_to })
        }
      }
      await nodeApi.updateDmaConfig(collectorId, allConfigs)

      // 刷新真实数据
      await fetch(collectorId)
    } catch (e: any) {
      delete overrideState.value[dmaId]
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
