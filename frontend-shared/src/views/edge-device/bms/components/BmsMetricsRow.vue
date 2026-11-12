<template>
  <!-- 指标卡 -->
  <section class="row metrics">
    <MetricStatCard label="SOC 剩余电量" :value="fmt(soc, 1)" unit="%" tone="success" :progress="socProgress" sub-text="健康区间 20% – 90%" :loading="realtimeLoading">
      <template #icon><el-icon :size="22"><PieChart /></el-icon></template>
    </MetricStatCard>
    <MetricStatCard label="剩余容量" :value="fmt(remainingCapacity, 2)" unit="Ah" tone="primary" :sub-text="capacitySubText" :loading="realtimeLoading">
      <template #icon><el-icon :size="22"><Odometer /></el-icon></template>
    </MetricStatCard>
    <MetricStatCard label="总电压" :value="fmt(totalVoltage, 2)" unit="V" tone="primary" :sub-text="voltageSubText" :loading="realtimeLoading">
      <template #icon><el-icon :size="22"><DataLine /></el-icon></template>
    </MetricStatCard>
    <MetricStatCard label="电流" :value="fmt(currentDisplay, 2)" unit="A" tone="primary" :sub-text="currentHint" :loading="realtimeLoading">
      <template #icon><el-icon :size="22"><Lightning /></el-icon></template>
    </MetricStatCard>
  </section>
</template>

<script setup lang="ts">
import { PieChart, Odometer, DataLine, Lightning } from '@element-plus/icons-vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
import { fmt } from '../bmsFormat'

/**
 * 顶部四张指标卡（自包含片段）。
 *
 * 边界：只负责"把页面算好的数值按统一口径排版"。
 *   · 数值派生（soc / 剩余容量 / 总电压 / 电流 与各自的副文本）在 useBmsRealtime；
 *   · :loading 由页面四态（realtimeLoading）驱动 —— 首屏骨架是**页面级**契约
 *     （BmsLoadingStates.spec 以「4 张卡全部接入 loading」断言），故四张卡必须同进同出，
 *     不能某一张自己判断；
 *   · fmt() 统一走 bmsFormat：未知一律 '—'，不允许各卡各写一套。
 */
defineProps<{
  soc: number | null
  socProgress: number | undefined
  remainingCapacity: number | null
  totalVoltage: number | null
  currentDisplay: number | null
  capacitySubText: string
  voltageSubText: string
  currentHint: string
  realtimeLoading: boolean
}>()
</script>

<style scoped>
/*
  四个指标卡：等权重自动折行。

  ⚠ flex-basis 与 min-width 的取值有实测依据（2026-10-20 二次复审修正）：
    v2 用 flex-basis:140 / min-width:0 → 在 **769~809px** 区间容器约 473px，
    140*3 + gap32 = 452 < 473 ⇒ 排成 3+1，每张仅 147px ⇒ 「8.34Ah」「51.71V」
    被省略号吃掉（实测 63>33 / 67>33）。769px 只比 768px 宽 1px 就崩，属悬崖式回归。
    修正：basis 回到 160、min-width 回到 150（放不下就折成 2 列而不是硬塞 3 列），
    窄屏 2 列由下方 @media (max-width:768px) 显式接管。 */
.metrics > .metric-card { flex: 1 1 160px; min-width: 150px; }

/* 平板/窄桌面（769~1100）：指标卡固定 2 列。
   为什么需要单独一档：这个区间容器宽度刚好能塞下 3 张 160px 的卡，
   但每张只剩 ~147px，数值会被省略号吃掉（复审实测 N1）。宁可 2 列宽松，
   不要 3 列挤爆。 */
@media (max-width: 1100px) {
  .metrics > .metric-card { flex: 1 1 calc(50% - 8px); min-width: 0; }
}

@media (max-width: 768px) {
  /* ── 指标卡：窄屏 2×2（此前是 1 列 × 4 行，白占 386px 高度） ──
     用 flex-basis 50% 而非 grid：与上面的 .row 流式模型保持一致。
     实测目标：区块 226 → ≤180px（4 张卡 2 行，每行约 82px）。 */
  .metrics > .metric-card { flex: 1 1 calc(50% - 8px); min-width: 0; }

  /*
    指标卡内部再压一档：MetricStatCard 的移动端样式是按"卡片独占一行"设计的
    （padding 8px 4px + 图标 34px），2×2 之后每张只有 ~186px 宽，
    「SOC 剩余电量」「剩余容量」这类 5 字标签 + 数值 + 副文本会显得拥挤。
      ① 收紧左内距（图标靠边）；
      ② 图标 34→28px，给文字腾 6px；
      ③ 数值 20→18px、标签 13→12px，保证「51.76V」这类四位数不折行。
  */
  /*
    同一行两张卡必须等高：实测出现 83 / 76 两种高度（SOC 卡有 progress 多 6px，
    总电压卡只有一行副文本）。虽然 flex 的 align-items:stretch（继承自 .row.metrics）
    已经让底边对齐，但**卡片自身高度仍不同**，视觉上像"一张卡悬空"。
    这里给固定 min-height 让两张卡实体等高，内容少的居中。
  */
  .row.metrics :deep(.metric-card) { padding: 8px 6px; gap: 8px; min-height: 84px; }
  .row.metrics :deep(.metric-icon) { width: 28px; height: 28px; border-radius: 7px; }
  .row.metrics :deep(.metric-icon .el-icon) { font-size: 18px; }
  .row.metrics :deep(.metric-label) { font-size: 12px; }
  .row.metrics :deep(.metric-value) { font-size: 18px; }
  .row.metrics :deep(.metric-unit) { font-size: 12px; }
  .row.metrics :deep(.metric-sub) { font-size: 11px; line-height: 16px; }
  .row.metrics :deep(.metric-aux) { height: auto; min-height: 16px; margin-top: 2px; }

  /*
    极窄屏（≤360px）改回单列：2 列时每张仅 116px，
    「SOC 剩余电量」「充电中（电流为正）」这类标签/副文本会被压到折行或截断。
    360px 以下本身已是小屏设备，单列可读性优先于紧凑度。
  */
  @media (max-width: 360px) {
    .metrics > .metric-card { flex: 1 1 100%; }
  }
}
</style>

<style scoped src="./bmsDetail.shared.css"></style>
