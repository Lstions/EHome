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

      <!-- 运行标志（非告警）：「有输出 / PV 馈能」是设备工作状态，不是故障。
           放在同一行右侧（margin-left:auto）：桌面不增加卡片高度（改前 235 → 改后仍 235），
           窄屏 status-header 允许换行，自动落到第二行。 -->
      <div v-if="runningFlags.length" class="run-flags">
        <div v-for="f in runningFlags" :key="f.key" class="run-flag" :class="{ on: f.on }">
          <span class="rf-label">{{ f.label }}</span>
          <span class="rf-value">{{ f.text }}</span>
        </div>
      </div>
    </div>

    <!-- Alarm grid（只含 10 个真告警位） -->
    <StatusItemGrid
      :items="alarmItems"
      active-text="异常"
      inactive-text="正常"
      empty-text="无告警状态数据"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import StatusItemGrid from '@/components/common/StatusItemGrid.vue'
import type { StatusItem } from '@/components/common/StatusItemGrid.vue'
import { INVERTER_RUNNING_FLAGS } from './runningFlags'

const props = defineProps<{
  // 后端实时数据可能尚未到达（此时为 null），组件内部已按 `!props.latestData` 兜底；
  // 类型上放开 null 以与实际运行时形态一致。
  latestData: Record<string, any> | null
}>()

/**
 * 工作模式解码。
 *
 * ⚠ 数值与字母的对应关系来自**驱动**而不是协议文档，两边必须一致：
 *   backend/internal/drivers/inverter_techfine.go:516
 *   workModeMap = {'P':0,'S':1,'L':2,'B':3,'F':4,'D':5,'X':6}
 *   协议原文（同文件 :510）：P=Power-on S=Standby L=Line B=Battery F=Fault D=Shutdown X=Test
 *
 * 一旦驱动改动该映射而这里没跟，界面会把"电池模式"显示成"故障"——
 * 这类错位不会报错，只会静默地说错话，故把字母一并写进标签便于核对。
 */
const WORK_MODE_MAP: Record<number, string> = {
  0: '开机 Power-on',
  1: '待机 Standby',
  2: '市电 Line',
  3: '电池 Battery',
  4: '故障 Fault',
  5: '关机 Shutdown',
  6: '测试 Test',
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

/** 真告警位（10 个）：1 = 异常。运行标志位（alarm_output / alarm_pv_to_load）
 *  不在此列 —— 它们的语义是工作状态，判定与文案见 ./runningFlags.ts。 */
const ALARM_FIELDS: Array<{ key: string; label: string }> = [
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

const alarmItems = computed<StatusItem[]>(() => {
  const data = props.latestData
  if (!data) return []
  return ALARM_FIELDS
    .filter(f => data[f.key] !== undefined)
    .map(f => ({
      key: f.key,
      label: f.label,
      active: data[f.key] === 1 || data[f.key] > 0,
    }))
})

/** 运行标志（非告警）：只在后端确实上报了该字段时显示 */
const runningFlags = computed(() => {
  const data = props.latestData
  if (!data) return []
  return INVERTER_RUNNING_FLAGS
    .filter(f => data[f.key] !== undefined)
    .map(f => {
      const on = data[f.key] === 1 || data[f.key] > 0
      return { key: f.key, label: f.label, text: on ? f.onText : f.offText, on }
    })
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

/* 运行标志：中性色，**不用红/绿** —— 它不是告警，不能让用户以为出了问题。
   只有「有输出 / 馈能中」这种功能性为真时用主色描边，表示设备在工作。 */
.run-flags {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  /* 贴在状态行右侧：桌面不新增一行，卡片高度与改前一致 */
  margin-left: auto;
}
.run-flag {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  /* 与同排 el-tag size=small（24px）等高，避免状态行被撑高 */
  min-height: 24px;
  padding: 0 8px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter);
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.run-flag.on {
  border-color: var(--el-color-primary-light-5);
  background: var(--el-color-primary-light-9);
  color: var(--el-text-color-regular);
}
.rf-label {
  color: var(--el-text-color-secondary);
}
.rf-value {
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.run-flag.on .rf-value {
  color: var(--el-color-primary);
}

@media (max-width: 768px) {
  .inverter-status-grid { padding: 0; }
  /* 告警列表窄屏 2 列：10 项竖排各 48px = 480px，2 列后约 240px。
     用 :deep 覆盖共享组件 StatusItemGrid —— 只在本卡片作用域内生效，
     不影响 BMS 保护状态卡（那边由父页 .protect-grid 自己控制列数）。 */
  .inverter-status-grid :deep(.status-item-grid) {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
    grid-auto-rows: minmax(40px, auto);
  }
  .inverter-status-grid :deep(.grid-item) {
    padding: 6px 8px;
    gap: 6px;
    min-width: 0;
    flex-wrap: wrap;
  }
  .inverter-status-grid :deep(.grid-label) {
    font-size: 12px;
    /* 中文不逐字竖排；放不下整词换行，绝不 ellipsis（信息比对齐重要） */
    word-break: keep-all;
    overflow-wrap: anywhere;
    min-width: 0;
  }
  .inverter-status-grid :deep(.grid-item :deep(.el-tag)) {
    font-size: 10px;
    height: 18px;
    line-height: 16px;
    padding: 0 4px;
  }
  .status-header { gap: 12px; margin-bottom: 10px; }
  .status-header .status-item { flex-wrap: wrap; gap: 6px; }
  .run-flags { gap: 6px; margin-left: 0; flex-basis: 100%; }
  .run-flag { padding: 6px 10px; font-size: 12px; }
}
</style>
