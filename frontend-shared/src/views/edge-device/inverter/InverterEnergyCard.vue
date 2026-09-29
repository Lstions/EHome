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

.energy-value {
  margin: 0;
  font-size: 26px;
  font-weight: 700;
  color: var(--el-text-color-primary);
  line-height: 1.2;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.energy-unit {
  font-size: 15px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
  margin-left: 4px;
}
</style>
