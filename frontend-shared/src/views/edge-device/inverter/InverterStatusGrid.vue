<template>
  <div class="inverter-status-grid">
    <!-- Work mode & fault code -->
    <div class="status-header">
      <div class="status-item">
        <span class="status-label">工作模式</span>
        <el-tag :type="workModeTagType" effect="dark" size="small">
          {{ workModeLabel }}
        </el-tag>
      </div>
      <div class="status-item">
        <span class="status-label">故障代码</span>
        <el-tag :type="hasFault ? 'danger' : 'success'" effect="plain" size="small">
          {{ hasFault ? `#${faultCode}` : '无故障' }}
        </el-tag>
      </div>
    </div>

    <!-- Alarm grid -->
    <div class="alarm-grid">
      <div
        v-for="alarm in alarmItems"
        :key="alarm.key"
        class="alarm-item"
        :class="{ active: alarm.active }"
      >
        <el-icon :size="18" :color="alarm.active ? THEME_COLORS.danger : THEME_COLORS.success">
          <component :is="alarm.active ? WarningFilled : CircleCheck" />
        </el-icon>
        <span class="alarm-label">{{ alarm.label }}</span>
        <el-tag size="small" :type="alarm.active ? 'danger' : 'success'" effect="plain">
          {{ alarm.active ? '异常' : '正常' }}
        </el-tag>
      </div>
    </div>

    <div v-if="!hasAlarmData" class="no-alarm">
      <el-empty description="无告警状态数据" :image-size="60" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { WarningFilled, CircleCheck } from '@element-plus/icons-vue'
import { THEME_COLORS } from '@/utils/theme'

const props = defineProps<{
  latestData: Record<string, any>
}>()

// Work mode decoding: numeric encoding from backend
// 0=初始上电, 1=待机, 2=市电, 3=电池, 4=故障, 5=关机, 6=测试
const WORK_MODE_MAP: Record<number, string> = {
  0: '初始上电',
  1: '待机',
  2: '市电',
  3: '电池',
  4: '故障',
  5: '关机',
  6: '测试',
}

const workModeLabel = computed(() => {
  const mode = props.latestData?.work_mode
  if (mode === undefined || mode === null || mode === '') return '未知'
  const modeNum = Number(mode)
  return WORK_MODE_MAP[modeNum] ?? `未知(${mode})`
})

const workModeTagType = computed(() => {
  const mode = Number(props.latestData?.work_mode ?? -1)
  if (mode === 4) return 'danger'   // 故障
  if (mode === 5) return 'info'     // 关机
  if (mode === 3) return 'warning'  // 电池
  return 'success'
})

const faultCode = computed(() => {
  return props.latestData?.fault_code ?? props.latestData?.error_code ?? 0
})

const hasFault = computed(() => faultCode.value > 0)

// 12 alarm fields
const ALARM_FIELDS: Array<{ key: string; label: string }> = [
  { key: 'alarm_pv_to_load', label: 'PV馈能负载' },
  { key: 'alarm_output', label: '有输出' },
  { key: 'alarm_battery_low', label: '电池低电报警' },
  { key: 'alarm_battery_missing', label: '电池未接' },
  { key: 'alarm_overload', label: '输出过载' },
  { key: 'alarm_overtemp', label: '机器过温' },
  { key: 'alarm_eeprom_data', label: 'EEPROM数据异常' },
  { key: 'alarm_eeprom_rw', label: 'EEPROM读写异常' },
  { key: 'alarm_pv_low', label: 'PV功率过低' },
  { key: 'alarm_input_overvoltage', label: '输入电压过高' },
  { key: 'alarm_battery_overvoltage', label: '电池电压过高' },
  { key: 'alarm_fan_error', label: '风扇异常' },
]

const hasAlarmData = computed(() => {
  if (!props.latestData) return false
  return ALARM_FIELDS.some(f => props.latestData[f.key] !== undefined)
})

const alarmItems = computed(() => {
  if (!props.latestData) return []
  return ALARM_FIELDS
    .filter(f => props.latestData[f.key] !== undefined)
    .map(f => ({
      key: f.key,
      label: f.label,
      active: props.latestData[f.key] === 1 || props.latestData[f.key] > 0,
    }))
})
</script>

<style scoped>
.inverter-status-grid {
  padding: 4px 0;
}

.status-header {
  display: flex;
  gap: 24px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}

.status-item {
  display: flex;
  align-items: center;
  gap: 8px;
}

.status-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.alarm-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 12px;
}

.alarm-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
  border: 1px solid var(--el-border-color-lighter);
}

.alarm-item.active {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger-light-5);
}

.alarm-label {
  flex: 1;
  font-size: 13px;
}

.no-alarm {
  grid-column: 1 / -1;
}
</style>
