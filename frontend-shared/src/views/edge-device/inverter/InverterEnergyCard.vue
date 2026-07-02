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

const props = defineProps<{
  latestData: Record<string, any>
}>()

const energyItems = computed(() => [
  { key: 'daily_energy', label: '日发电量', icon: Sunrise, class: 'daily' },
  { key: 'monthly_energy', label: '月发电量', icon: Calendar, class: 'monthly' },
  { key: 'yearly_energy', label: '年发电量', icon: TrendCharts, class: 'yearly' },
  { key: 'total_energy', label: '总发电量', icon: DataAnalysis, class: 'total' },
])

function formatEnergy(v: any): string {
  if (v === undefined || v === null || isNaN(v)) return '--'
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
  color: #fff;
}

.energy-card.daily .energy-icon {
  background: linear-gradient(135deg, var(--el-color-warning), var(--el-color-warning-light-3));
}

.energy-card.monthly .energy-icon {
  background: linear-gradient(135deg, var(--el-color-primary), var(--el-color-primary-light-3));
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
