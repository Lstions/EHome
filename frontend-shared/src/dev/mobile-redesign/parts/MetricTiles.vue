<!--
  4 个指标卡「移动端满宽」版本 —— BMS 移动端改版 demo 专用（src/dev）。

  ── 它修的是什么（用户批评 1 / 6：没利用满空间）──────────────────────────────
  生产页在 393px 下是 2×2 的 MetricStatCard，卡片间距 + el-main padding 后
  每张卡内容区有效宽度只有约 97px（组件注释自己记着这个数），
  于是「健康区间 20% – 90%」「充电中（电流为正）」被迫折成 2~3 行，
  四张卡总高 386px，且左右各留 32/40px 空白。

  ── 两种布局 ──────────────────────────────────────────────────────────────
    · tiles  2×2 等分满宽（每张 ≈ (宽-8)/2），数值 20px、副文本**单行省略**，
             信息密度靠"每卡只说一件事 + 极简副文本"
    · strip  单行四列（规范 §4.3「统计卡」第 3 条推荐的形态）：
             每格只有 标签 + 数值 + 单位，图标 22px，数值 16px，
             四张卡总高 ~78px —— 把四块信息压成一条仪表读数带
    · sheet  2×2，但把"判定性"信息（SOC 进度条、电流方向）做成卡底细线，
             数值区完全留给数字
-->
<template>
  <div class="metric-tiles" :class="`v-${variant}`">
    <!-- SOC -->
    <div class="tile">
      <div class="tile-head">
        <el-icon :size="14" class="tile-icon tone-success"><PieChart /></el-icon>
        <span class="tile-label">剩余电量</span>
      </div>
      <div class="tile-value">
        <span class="num">{{ METRICS.soc.toFixed(1) }}</span><span class="unit">%</span>
      </div>
      <div v-if="variant !== 'strip'" class="tile-foot">
        <span class="foot-text">健康区间 {{ METRICS.socHealthy[0] }}–{{ METRICS.socHealthy[1] }}%</span>
      </div>
      <!--
        进度条贴卡底：不占行高，也不与文字抢空间。
        7% 的 SOC 在整宽进度条上只剩 11px，看起来像"条坏了" —— 故补一条
        健康下限标记（20%），让"还差多远"可读，而不是一根几乎看不见的短线。
      -->
      <!--
        strip 档（单行四格）不画任何底条：四格只有 52px 高，3px 的条会贴着数值下沿，
        7% 的 SOC 只剩 ~6px 长，读起来是"渲染坏了"而不是"电量低"。
        判定信息在该档由数值 + 卡头文案承担，不靠条。
      -->
      <div v-if="variant !== 'strip'" class="tile-bar" :class="socBarTone" role="img"
        :aria-label="`剩余电量 ${METRICS.soc}%，健康下限 ${METRICS.socHealthy[0]}%`">
        <span class="tile-bar-fill" :style="{ width: Math.min(METRICS.soc, 100) + '%' }"></span>
        <span class="tile-bar-mark" :style="{ left: METRICS.socHealthy[0] + '%' }" aria-hidden="true"></span>
      </div>
    </div>

    <!-- 剩余容量 -->
    <div class="tile">
      <div class="tile-head">
        <el-icon :size="14" class="tile-icon tone-primary"><Odometer /></el-icon>
        <span class="tile-label">剩余容量</span>
      </div>
      <div class="tile-value">
        <span class="num">{{ METRICS.remaining.toFixed(2) }}</span><span class="unit">Ah</span>
      </div>
      <div v-if="variant !== 'strip'" class="tile-foot">
        <span class="foot-text">标称 {{ METRICS.nominal }} Ah</span>
      </div>
      <!-- 其余卡底条只在 tiles/sheet 档作为"量感"保留；strip 档四格并排时纯装饰，去掉 -->
      <div v-if="variant !== 'strip'" class="tile-bar is-muted">
        <span class="tile-bar-fill" :style="{ width: (METRICS.remaining / METRICS.nominal * 100).toFixed(1) + '%' }"></span>
      </div>
    </div>

    <!-- 总电压 -->
    <div class="tile">
      <div class="tile-head">
        <el-icon :size="14" class="tile-icon tone-warning"><DataLine /></el-icon>
        <span class="tile-label">总电压</span>
      </div>
      <div class="tile-value">
        <span class="num">{{ METRICS.totalVoltage.toFixed(2) }}</span><span class="unit">V</span>
      </div>
      <div v-if="variant !== 'strip'" class="tile-foot">
        <span class="foot-text">{{ DEVICE.cellCount }} 串 · 均值 {{ cellAvg.toFixed(3) }} V</span>
      </div>
      <div v-if="variant !== 'strip'" class="tile-bar is-muted">
        <span class="tile-bar-fill" :style="{ width: '72%' }"></span>
      </div>
    </div>

    <!-- 电流 -->
    <div class="tile">
      <div class="tile-head">
        <el-icon :size="14" class="tile-icon tone-primary"><Lightning /></el-icon>
        <span class="tile-label">电流</span>
      </div>
      <div class="tile-value">
        <span class="num">{{ METRICS.current.toFixed(2) }}</span><span class="unit">A</span>
      </div>
      <div v-if="variant !== 'strip'" class="tile-foot">
        <!-- 方向是文字 + 颜色双通道；充电绿，不写成告警色 -->
        <span class="foot-text is-charge">充电中</span>
      </div>
      <div v-if="variant !== 'strip'" class="tile-bar is-muted">
        <span class="tile-bar-fill is-charge" :style="{ width: chargeBarPercent + '%' }"></span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { PieChart, Odometer, DataLine, Lightning } from '@element-plus/icons-vue'
import { METRICS, DEVICE, CELL_STATS } from '../mockBms'

withDefaults(defineProps<{
  /** tiles / strip / sheet */
  variant?: 'tiles' | 'strip' | 'sheet'
}>(), {
  variant: 'tiles',
})

const cellAvg = computed(() => CELL_STATS.avg)
/** SOC 低于健康下限 → warning 色（不是 danger：低电量是工况，不是故障） */
const socBarTone = computed(() =>
  METRICS.soc < METRICS.socHealthy[0] ? 'is-warning' : 'is-success')
/** 电流条：以 5A 为满刻度，仅表"有电流且方向"，不作为量程读数 */
const chargeBarPercent = computed(() =>
  Math.min(Math.abs(METRICS.current) / 5 * 100, 100).toFixed(0))
</script>

<style scoped>
.metric-tiles {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.tile {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
  /* 左右内边距 10px：393px 下每张卡 168px 宽，内容区 148px —— 比生产页的 97px 宽 50% */
  padding: 9px 10px 10px;
  background: var(--card-bg);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--radius-md);
  /* 底部进度条绝对定位用 */
  overflow: hidden;
}

.tile-head {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
}
.tile-icon { flex-shrink: 0; }
.tone-success { color: var(--el-color-success); }
.tone-primary { color: var(--el-color-primary); }
.tone-warning { color: var(--el-color-warning); }
.tile-label {
  font-size: 11px;
  color: var(--el-text-color-secondary);
  /* 中文标签不逐字竖排 */
  word-break: keep-all;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.tile-value {
  display: flex;
  align-items: baseline;
  gap: 1px;
  min-width: 0;
  line-height: 1.15;
}
.num {
  font-size: 21px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  /* 关键数值不得被省略号截断（规范 §4.2.5） */
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.unit {
  font-size: 11px;
  color: var(--el-text-color-secondary);
  margin-left: 1px;
}

.tile-foot {
  min-width: 0;
  /* 单行省略而不是折行：折行会把 2×2 拉成不等高，且这里的副文本都是"补充说明" */
  overflow: hidden;
}
.foot-text {
  display: block;
  font-size: 10px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.foot-text.is-charge { color: var(--el-color-success); }

/* 卡底 3px 进度条：贴边、不占布局高度 */
.tile-bar {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 3px;
  background: var(--el-fill-color);
}
.tile-bar-fill {
  display: block;
  height: 100%;
  border-radius: 0 2px 2px 0;
  background: var(--el-color-success);
  transition: width 0.3s;
}
/* 健康下限刻线：细竖线，只在 SOC 卡出现（其余卡的底条是纯量感，无阈值语义） */
.tile-bar-mark {
  position: absolute;
  top: -1px;
  bottom: -1px;
  width: 1px;
  background: var(--el-text-color-placeholder);
}
.tile-bar.is-success .tile-bar-fill { background: var(--el-color-success); }
.tile-bar.is-warning .tile-bar-fill { background: var(--el-color-warning); }
.tile-bar.is-muted .tile-bar-fill { background: var(--el-color-info); }
.tile-bar-fill.is-charge { background: var(--el-color-success); }

/* ── strip：单行四列信息带 ── */
.v-strip {
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 6px;
}
.v-strip .tile {
  align-items: center;
  justify-content: center;
  gap: 1px;
  /* 单行带：每格只剩标签 + 数值；无底条，上下内边距对称 */
  padding: 8px 4px;
  text-align: center;
}
.v-strip .tile-head { justify-content: center; gap: 3px; }
.v-strip .tile-icon { display: none; }
.v-strip .tile-label { font-size: 10px; }
.v-strip .num { font-size: 17px; }
.v-strip .unit { font-size: 10px; }

/* ── sheet：进度条不做装饰，只保留 SOC 的判定线 ── */
.v-sheet .tile { padding-bottom: 11px; }
.v-sheet .tile-foot { display: block; }
</style>
