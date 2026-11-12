<template>
  <!-- 温度探头：内容驱动卡，与 MOS/保护等卡同属流式（auto-fill 列数） -->
  <section class="row temp-row">
    <div class="card temp-card">
      <div class="card-head">
        <span class="card-title">温度探头</span>
        <span class="card-hint">{{ realtimeLoading ? '加载中…' : temps.length + ' 路' }}</span>
      </div>
      <div class="temp-list">
        <div v-for="t in temps" :key="t.index" class="temp-item">
          <span class="temp-label">探头{{ t.index }}</span>
          <span class="temp-value" :class="'is-' + t.level">{{ t.value.toFixed(1) }} ℃</span>
          <el-tag :type="t.level === 'success' ? 'success' : t.level === 'warning' ? 'warning' : 'danger'" size="small" effect="plain">
            {{ t.text }}
          </el-tag>
        </div>
        <div v-if="!temps.length" class="stream-empty">{{ realtimeLoading ? '正在加载温度数据…' : realtimeError ? '温度数据加载失败' : '暂无温度数据' }}</div>
      </div>
      <p class="card-footnote">&gt;45℃ 偏高 · &gt;60℃ 过高</p>
    </div>
  </section>
</template>

<script setup lang="ts">
/**
 * 温度探头卡（自包含片段）。
 *
 * 边界：只渲染页面 useBmsRealtime 已经算好的 temps（每路探头的值/等级/结论词），
 * 本组件不含取数逻辑 —— 阈值（>45 偏高 / >60 过高）属于**展示口径**，
 * 与页面上「温度趋势」卡共用同一组阈值语义，但趋势图在 diag/TempTrendCard.vue。
 *
 * 空态文案三态与页面其它卡一致：加载中 / 加载失败 / 确实没有数据（fail-open，不能藏）。
 */
interface TempProbe { index: number; value: number; level: 'success' | 'warning' | 'danger'; text: string }

defineProps<{
  temps: TempProbe[]
  realtimeLoading: boolean
  realtimeError: boolean
}>()
</script>

<style scoped>
/* ══════════ 温度 ══════════ */
.temp-row > .temp-card         { flex: 1 1 100%; }   /* 与 .op-groups 同宽，内部 auto-fill 决定列数 */
/* 流式：温度卡按宽度自适应列数（写死 2 列在窄屏会挤成两行文字）。 */
.temp-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 8px; }
.temp-item { display: flex; align-items: center; gap: 8px; padding: 10px 12px; background: var(--el-fill-color-lighter); border-radius: var(--radius-sm); min-width: 0; }
.temp-label { font-size: 12px; color: var(--el-text-color-regular); white-space: nowrap; }
.temp-value { font-size: 16px; font-weight: 600; margin-left: auto; white-space: nowrap; }
.temp-value.is-success { color: var(--el-color-success); }
.temp-value.is-warning { color: var(--el-color-warning); }
.temp-value.is-danger { color: var(--el-color-danger); }

@media (max-width: 768px) {
  /* ── 温度探头：240 → ≈170px ──
     桌面 auto-fill 在 295px 下退化成 1 列（minmax 150px × 2 > 295），4 个探头竖排 160px。
     窄屏固定 2 列、数值与状态同排，行高 44px（触摸目标不变）。 */
  .temp-card > .card-head { margin-bottom: 6px; }
  .temp-list { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px; }
  .temp-item { padding: 6px 8px; gap: 6px; }
  .temp-label { font-size: 11px; }
  .temp-value { font-size: 15px; }
  /* 同 BmsStatusCards 的说明：与页面 .page-body :deep(.el-tag) 同特异性，拆开后顺序不可依赖，
     多带一级 .temp-card 保证「内距 4px」与拆分前一致。 */
  .temp-card .temp-item :deep(.el-tag) { font-size: 10px; height: 18px; line-height: 16px; padding: 0 4px; }
  .temp-card > .card-footnote { font-size: 10px; margin-top: 6px; }
  .temp-card { flex: 1 1 100%; min-width: 0; }
}
</style>

<style scoped src="./bmsDetail.shared.css"></style>
