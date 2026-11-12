<template>
        <section class="card dma-card">
          <div v-if="dmaLoading" class="card-loading"><el-skeleton :rows="3" animated /></div>
          <!-- D5：DMA 与其它总线不同——设备可能**根本没上报 DMA 资源**
               （本仓实测 nodes.capabilities.buses.dma 缺失，而 uart/spi 却报 dma_supported=true，
                 属设备侧数据矛盾）。此时既不能给"点不动的空控件"，也不能只说"暂无"：
                 必须区分「设备未上报」与「上报了但一条都没有」，否则用户会把
                 "节点没有这个能力"读成"这台设备确实没有 DMA"。 -->
          <div v-else-if="dmaChannels.length === 0" class="card-empty dma-empty" data-dma-empty>
            <template v-if="dmaNotReported">
              该节点未上报 DMA 资源（设备能力上报中没有 dma 项）—— 这不是"没有 DMA 通道"，而是本节点未提供该能力信息。
            </template>
            <template v-else>该节点已上报 {{ dmaReportedCount }} 条 DMA 资源，但当前没有可用的 DMA 通道信息</template>
          </div>
          <div v-else class="dma-grid">
            <div v-for="dma in dmaChannels" :key="dma.dma_id" class="dma-item">
              <div class="dma-item-head">
                <span class="mono">{{ dma.name || `DMA${dma.dma_id}` }}</span>
                <span class="bus-tag" :class="dmaTagClass(dma.state)">{{ dmaStateText(dma.state) }}</span>
              </div>
              <div class="dma-item-row"><span>通道类型</span><b class="mono">{{ dmaTypeText(dma.dma_type) }}</b></div>
              <div class="dma-item-row"><span>支持能力</span><b class="mono">{{ capText(dma.capabilities) }}</b></div>
              <div class="dma-item-row"><span>最大突发长度</span><b class="mono">{{ dma.max_burst }}</b></div>
              <div class="dma-item-row"><span>DMA 绑定</span><b class="mono">{{ dmaBoundToText(dma.bound_to) }}</b></div>
              <div class="dma-item-row"><span>可服务总线</span><b class="mono">{{ busText(dma.compatible_bus) }}</b></div>
            </div>
          </div>
        </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { Capabilities, DmaChannelInfo } from '@/api/node'
import { dmaStateText } from '@/utils/dmaState'
import { busText, capText, dmaBoundToText, dmaTagClass, dmaTypeText } from '../composables/busResourceHelpers'

/**
 * 「DMA 通道」页签：只读资源视图（绑定操作在「总线配置」里做）。
 *
 * 为什么单独成组件：它的空态判据（是否上报了 buses.dma）与其它页签不同，
 * 而且这一页没有任何写操作 —— 把它和可写的总线配置混在一个文件里，
 * 很容易在改总线写逻辑时误碰这里的空态口径。
 */
const props = defineProps<{
  dmaChannels: DmaChannelInfo[]
  dmaLoading: boolean
  /** 能力上报：用于区分「设备未上报 dma」与「上报了但一条不可用」。 */
  capabilities: Capabilities
}>()

// D5：设备能力上报里是否存在 dma 项。缺失 = 设备未上报该能力（而不是"上报了 0 条"）。
const dmaNotReported = computed(() => {
  const buses = (props.capabilities as any)?.buses
  return !buses || buses.dma === undefined || buses.dma === null || (Array.isArray(buses.dma) && buses.dma.length === 0)
})
/** 设备在能力上报里声明的 DMA 资源条数（用于区分"未上报"与"上报了但拿不到通道"）。 */
const dmaReportedCount = computed(() => {
  const dma = (props.capabilities as any)?.buses?.dma
  return Array.isArray(dma) ? dma.length : 0
})
</script>

<style scoped>
/* DMA 卡片：窄屏单列规则在 nodeOverview.shared.css（唯一 768 块）。 */
/* DMA 通道卡片网格 */
.dma-card { padding: 16px 20px; }
.dma-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
.dma-item {
  min-width: 0; border: 1px solid var(--no-border-light); border-radius: 6px; padding: 12px 14px;
  display: flex; flex-direction: column; gap: 6px; background: var(--card-bg, #fff);
}
.dma-item-head { min-height: 24px; display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 4px; }
.dma-item-head > span:first-child { color: var(--no-text); font-size: 14px; font-weight: 600; line-height: 20px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.dma-item-head .bus-tag { margin-right: 0; flex: 0 0 auto; }
.dma-item-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; line-height: 20px; }
.dma-item-row span { color: var(--no-text-secondary); font-size: 12px; line-height: 18px; }
.dma-item-row b { color: var(--no-text); font-weight: 500; text-align: right; overflow-wrap: anywhere; }
.dma-empty { line-height: 20px; }
</style>

<style scoped src="./nodeOverview.shared.css"></style>
