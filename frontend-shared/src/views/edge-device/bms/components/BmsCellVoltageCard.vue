<template>
  <!-- 电芯电压分布：图表卡是固定高度的"异类"，独占一行 ——
       与内容驱动卡片同行时，矮卡下方必然留出页面底色（padding 无法吸收固定高度差）。 -->
  <section class="row cell-chart-row">
    <div class="card cell-card">
      <div class="card-head">
        <span class="card-title">电芯电压分布</span>
        <span class="card-hint">{{ cells.length }} 串 · 单位 V</span>
        <span v-if="cells.length" class="head-right cell-summary">
          最高 <b class="txt-warning">#{{ maxCell.index }} {{ maxCell.voltage.toFixed(3) }}V</b>
          · 最低 <b class="txt-secondary">#{{ minCell.index }} {{ minCell.voltage.toFixed(3) }}V</b>
          · 压差 <b :class="diff > 0.1 ? 'txt-warning' : 'txt-secondary'">{{ diff.toFixed(3) }}V</b>
        </span>
        <span v-else class="head-right cell-summary">{{ realtimeLoading ? '正在加载单体电压…' : realtimeError ? '单体电压加载失败' : '暂无单体电压数据' }}</span>
      </div>
      <div class="cell-chart-host">
        <BmsCellVoltageChart
          v-if="cellVoltages.length"
          :voltages="cellVoltages"
          :cell-count="cellVoltages.length"
          :height="isMobileViewport ? '112px' : '300px'"
        />
        <div v-else class="cell-chart-empty">{{ realtimeLoading ? '正在加载单体电压…' : realtimeError ? '单体电压加载失败（下方已提供重试）' : '暂无单体电压数据' }}</div>
      </div>
      <div class="cell-legend">
        <span class="lg"><i class="swatch swatch-normal"></i>正常</span>
        <span class="lg"><i class="swatch swatch-high"></i>偏高 &gt; 3.600V</span>
        <span class="lg"><i class="swatch swatch-danger"></i>偏低 &lt; 3.000V</span>
        <span class="cell-thresholds">参考线 3.000V / 3.600V</span>
      </div>
      <p class="card-footnote">点击柱体可查看精确值。</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import BmsCellVoltageChart from '../BmsCellVoltageChart.vue'

/**
 * 电芯电压分布卡（自包含片段）。
 *
 * 边界：本卡只画「当前这一帧」的单体电压分布，数据取自页面的 useBmsRealtime
 * （cells / maxCell / minCell / diff / cellVoltages 都是同一份 latestData 的派生值）。
 * 历史趋势与运行趋势在 BmsChartsPanel 内，与这里无关。
 *
 * 高度是**本卡的硬不变量**：图表是固定高度的"异类"，容器高度变化会撑动整页
 * （页面依赖 overflow-anchor:none 防跳动），所以桌面 300px / 窄屏 112px 写死在样式里，
 * 并同步通过 :height 下发给 BmsCellVoltageChart（否则 300px 的图会溢出 112px 容器）。
 */
interface CellVoltage { index: number; voltage: number }

defineProps<{
  cells: CellVoltage[]
  maxCell: CellVoltage
  minCell: CellVoltage
  diff: number
  cellVoltages: number[]
  realtimeLoading: boolean
  realtimeError: boolean
  isMobileViewport: boolean
}>()
</script>

<style scoped>
/* ══════════ 电芯电压分布（图表复用旧页 BmsCellVoltageChart） ══════════ */
.cell-chart-row > .cell-card  { flex: 1 1 100%; }   /* 图表卡独占一行 */
.cell-summary { font-size: 12px; color: var(--el-text-color-secondary); }
/* 固定高度：图表异步渲染不得改变卡片高度（本页依赖 overflow-anchor:none 防跳动）。
   图表卡独占一行（.cell-chart-row），不再有"同排矮卡"要迁就，故恢复到 300px 的可读高度。 */
.cell-chart-host { height: 300px; }
.cell-chart-empty { height: 300px; display: flex; align-items: center; justify-content: center; color: var(--el-text-color-secondary); font-size: 12px; }
.cell-legend { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; margin-top: 10px; font-size: 12px; color: var(--el-text-color-secondary); }
.lg { display: inline-flex; align-items: center; gap: 5px; }
.swatch { width: 10px; height: 10px; border-radius: 2px; display: inline-block; }
/* 与 BmsCellVoltageChart 口径一致：正常=成功色，>3.6V=告警色，<3.0V=危险色 */
.swatch-normal { background: var(--el-color-success); }
.swatch-high { background: var(--el-color-warning); }
.swatch-danger { background: var(--el-color-danger); }
.cell-thresholds { margin-left: auto; }

@media (max-width: 768px) {
  /* ── 电芯电压分布：477 → ≈200px ──
     393px 下每根柱只有 ~24px，柱顶标签已降级为「只标越界/极值」，
     300px 高的图里大半是空白；卡头（最高/最低/压差）在 393px 会折成 3 行，图例也折 2 行。
     压法：图区 112px + 卡头 2 行 + 图例 1 行 + 脚注收起。
     最高/最低/压差全部保留（不截断），信息一条不少。 */
  .cell-chart-host, .cell-chart-empty { height: 112px; }
  .cell-card > .card-head { flex-wrap: wrap; row-gap: 2px; margin-bottom: 6px; }
  .cell-summary { font-size: 11px; line-height: 16px; }
  .cell-legend { gap: 8px; margin-top: 6px; font-size: 10px; }
  /* 「点击柱体可查看精确值」是触摸交互的提示，手机上点柱子本身即可发现，收起省 20px */
  .cell-card > .card-footnote { display: none; }
  /* 「参考线 3.000V / 3.600V」与图例里「偏高 > 3.600V / 偏低 < 3.000V」是同一组阈值的重复表述，
     窄屏只保留图例（它带色块，能直接对上柱子颜色），省一行 16px。 */
  .cell-thresholds { display: none; }
  .cell-thresholds { margin-left: 0; }
}
</style>

<style scoped src="./bmsDetail.shared.css"></style>
