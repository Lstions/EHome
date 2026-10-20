<template>
  <div class="energy-card-container">
    <div
      v-for="item in energyItems"
      :key="item.key"
      class="energy-card"
      :class="item.class"
    >
      <div class="energy-icon">
        <el-icon :size="24"><component :is="item.icon" /></el-icon>
      </div>
      <div class="energy-body">
        <p class="energy-label">{{ item.label }}</p>
        <p class="energy-value">
          {{ formatEnergy(latestData?.[item.key]) }}
          <span class="energy-unit">kWh</span>
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Sunrise, Calendar, DataAnalysis, TrendCharts } from '@element-plus/icons-vue'

defineProps<{
  // 后端实时数据可能尚未到达（此时为 null），模板已按可选链兜底；
  // 类型上放开 null 以与实际运行时形态一致。
  latestData: Record<string, any> | null
}>()

const energyItems = computed(() => [
  { key: 'daily_energy', label: '日发电量', icon: Sunrise, class: 'daily' },
  { key: 'monthly_energy', label: '月发电量', icon: Calendar, class: 'monthly' },
  { key: 'yearly_energy', label: '年发电量', icon: TrendCharts, class: 'yearly' },
  { key: 'total_energy', label: '总发电量', icon: DataAnalysis, class: 'total' },
])

function formatEnergy(v: any): string {
  if (v === undefined || v === null || isNaN(v)) return '—'
  const num = Number(v)
  if (num >= 10000) return num.toFixed(0)
  if (num >= 100) return num.toFixed(1)
  return num.toFixed(2)
}
</script>

<style scoped>
.energy-card-container {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 16px;
}

.energy-card {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 20px;
  border-radius: 12px;
  background: var(--el-fill-color-lighter);
  border: 1px solid var(--el-border-color-lighter);
  transition: transform 0.3s, box-shadow 0.3s;
}

.energy-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--el-box-shadow-light);
}

.energy-icon {
  width: 52px;
  height: 52px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  /* 前景由各变体按底色决定，见下（颜色只承载语义，不在这里写死）。 */
  color: var(--text-on-fill);
}

/*
  卡片间距：4 张卡在 1440 下每张 217px，图标 52 + gap 14 后数值区仅 109px。
  把 gap 收窄到 10px 并为数值区争取空间，配合下面 .energy-value 的字号自适应。
*/
.energy-card {
  gap: 10px;
  padding: 16px 14px;
}

/* 4 个变体的前景各不相同，因为底色亮暗走向相反：
   · daily(暖色)/yearly(绿)/total(灰) 用的语义色在**暗色下被提亮** ⇒ 前景必须转深
     （否则白图标 1.85–2.36:1）；这些色在亮色下够深，而 --text-on-fill 亮色即白 ⇒ 一个 token 两边都对。
   · monthly 走 primary 家族（#1f5ad8→#628ce4/#2f6ae8），**两端都是深色** ⇒ 必须保持白字
     （深色前景在 primary 上只有 2.91）。 */
.energy-card.daily .energy-icon {
  background: linear-gradient(135deg, var(--el-color-warning), var(--el-color-warning-light-3));
}

.energy-card.monthly .energy-icon {
  background: linear-gradient(135deg, var(--el-color-primary), var(--el-color-primary-light-3));
  /* 白字：primary 家族两端都是深色（亮 #1f5ad8→#628ce4 / 暗 #1f5ad8→#2f6ae8），
     暗色下白字最差 4.83:1 达标；深色前景反而只有 2.91，故此处保持白字。 */
  color: #fff;
}

.energy-card.yearly .energy-icon {
  background: linear-gradient(135deg, var(--el-color-success), var(--el-color-success-light-3));
}

.energy-card.total .energy-icon {
  background: linear-gradient(135deg, var(--el-color-info), var(--el-color-info-light-3));
}

.energy-body {
  flex: 1;
  min-width: 0;
}

.energy-label {
  margin: 0 0 4px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

/*
  发电量数值**不允许被截断**（2026-10-20 生产实测缺陷）：
  1440px 桌面下卡片 217px、图标占 52+14，数值区仅 109px，
  「2194.2 kWh」实宽 121px ⇒ 显示为「2194.2…」，「4595.8…」同样。
  发电量是这一屏最重要的数字，截成省略号等于没有这个功能。
  策略（按生效顺序）：
    ① 数值不再 nowrap 硬截断，改为允许收缩 + 字号自适应降档；
    ② 图标槽在窄卡里缩小，把宽度让给数字；
    ③ 卡片最小宽度抬高，避免落到会截断的宽度区间。
*/
.energy-card {
  min-width: 190px;
}

.energy-value {
  margin: 0;
  /*
    clamp 下限 18px：1440 桌面下数值区仅 ~109px，「2194.2 kWh」需要约 111px，
    23px 字号仍差 2px 被截。降到 18px 后 5 位数 + 单位能完整放下；
    宽屏（≥1600）自动回升到 26px，保持原有视觉重量。
  */
  font-size: clamp(18px, 1.35vw, 26px);
  font-weight: 700;
  color: var(--el-text-color-primary);
  line-height: 1.2;
  white-space: nowrap;
  /* 不再 ellipsis：万一仍超出（极端窄屏），宁可让它溢出可见也不吞掉数字 */
  overflow: visible;
  text-overflow: clip;
  font-variant-numeric: tabular-nums;
}

.energy-unit {
  font-size: 15px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
  margin-left: 4px;
}

/*
  窄屏（≤768px）移动端压缩布局（2026-10-20，生产实测缺陷）。
  缺陷：4 项发电量在 393px 下落到 **1 列竖排**（auto-fill minmax(200px,1fr) 放不下第二列），
  实测卡高 492px（组件根 392px），「日/月/年/总」四项要滚一整屏。
  修法：2×2 网格；每格仍「图标 + 标签 + 数值 kWh」横排，图标与字号按格子宽度降档，
  保证 5~7 位整数 + 单位在 ~150px 格宽内完整可见（**不截断、不换行**）。
  桌面（>768px）规则一律不动 —— 本块之外改前改后完全相同。
*/
@media (max-width: 768px) {
  .energy-card-container {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }
  /* 桌面为防截断设了 min-width:190px（见上），窄屏 2 列后必须放开，否则会撑破网格 */
  .energy-card { min-width: 0; gap: 8px; padding: 10px; }
  .energy-icon { width: 24px; height: 24px; border-radius: 6px; }
  .energy-label { margin-bottom: 2px; font-size: 12px; line-height: 16px; }
  .energy-value { font-size: 16px; line-height: 20px; }
  .energy-unit { font-size: 12px; margin-left: 2px; }
}
</style>
