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
  /* 2026-10-20 独立复核（WCAG 计算）：secondary #909399 on #fff = 3.1:1，13px 正文要求 ≥4.5:1。
     改 regular #606266 = 5.7:1。 */
  color: var(--el-text-color-regular);
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
  /* 浅色主题：primary #409eff on primary-light-9 = 2.4:1（复核实测），primary-dark-2 也只有 3.64:1，
     再深到 #1f5fa8 = 5.58:1。暗色主题必须换亮色（见下方 html.dark 覆盖），否则 2.5:1 更糟。 */
  color: #1f5fa8;
}
html.dark .run-flag.on .rf-value {
  /* 本项目暗色主题是自定义调色板：primary-light-3 #3375b9 偏暗（3.4:1），亮色在 dark-2（#66b1ff） */
  color: var(--el-color-primary-dark-2);
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
  /* 每格 148px 里要塞「图标 14 + 标签 + 状态徽标 26」，空间很紧。
     2026-10-20 生产暗色实测：图标 18 + gap 6 时「电池低电报警」被压成两行（「电池低电报/警」），
     同一行左右两格高度不一致，整卡看起来参差。收紧图标与间距让 6 字标签单行。 */
  .inverter-status-grid :deep(.grid-item) {
    padding: 6px 8px;
    gap: 4px;
    min-width: 0;
    flex-wrap: nowrap;
  }
  .inverter-status-grid :deep(.grid-item .el-icon) {
    flex: 0 0 auto;
    font-size: 14px;
  }
  .inverter-status-grid :deep(.grid-label) {
    flex: 1 1 auto;
    font-size: 12px;
    /* 中文按词整体排列：keep-all 禁止 CJK 逐字断行，且**不能**再写 overflow-wrap:anywhere
       （那条会覆盖 keep-all，把「电池低电报警」拆成「电池低电报/警」）。 */
    word-break: keep-all;
    min-width: 0;
  }
  .inverter-status-grid :deep(.grid-item .el-tag) {
    flex: 0 0 auto;
    font-size: 10px;
    height: 18px;
    line-height: 16px;
    padding: 0 4px;
    margin: 0;
  }
  /* 复核实测：success #67c23a on #fff = 2.2:1（12px 正文要求 4.5:1）。
     只在**本卡**把 plain 徽标文字加深（浅色主题 #2f6b12 = 6.0:1），
     不污染 BMS 保护卡与其它页面；暗色主题用亮绿（见下方 html.dark 覆盖）。 */
  .inverter-status-grid :deep(.grid-item .el-tag--success) {
    --el-tag-text-color: #2f6b12;
    --el-tag-border-color: #67c23a;
    --el-tag-bg-color: #f0f9eb;
  }
  .status-header { gap: 12px; margin-bottom: 10px; }
  .status-header .status-item { flex-wrap: wrap; gap: 6px; }
  .run-flags { gap: 6px; margin-left: 0; flex-basis: 100%; }
  .run-flag { padding: 6px 10px; font-size: 12px; }
}

/* 暗色主题：上面为「浅色底」算的加深色在深色底上反而读不清（#1f5fa8 on #18222b = 2.5:1，实测）。
   这里换回 Element Plus 的亮色 token —— 深色底 + 亮绿/亮蓝 ≈ 7~8:1。 */
html.dark .inverter-status-grid :deep(.grid-item .el-tag--success) {
  /* 同上：暗色主题的 light-3 是**更深**的绿（#4e8e2f，1.8:1），亮色在 --el-color-success（#85ce61） */
  --el-tag-text-color: var(--el-color-success);
  --el-tag-border-color: var(--el-color-success);
  --el-tag-bg-color: rgba(133, 206, 97, 0.12);
}
</style>
