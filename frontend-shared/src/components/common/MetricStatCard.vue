<!--
  MetricStatCard.vue — 设备详情页指标卡共享组件（统一样式，根治风格漂移）

  背景（2026-08-09 审计）：
  - BmsDetailPage / InverterDetailPage 此前各自用 el-card + 私有 CSS 重造指标卡，
    且 .metric-icon 被写成渐变底 + 白图标，违反 ui-consistency-standards.md
    「Metric Card Standard」（透明底彩色图标）。
  - 4 卡内容行数不同（SOC 有进度条 / 容量与电流有副文本 / 总电压仅两行），
    el-col 不设等高导致卡高不齐、底边错位 → 辅助槽永远占位解决。
  - 放电（电流为负）曾整体标 danger 红，但放电是正常运行方向 → direction 指示
    只用中性或 primary 色，tone='danger' 仅在真实告警时由调用方显式传入。

  防回退设计：本组件 CSS 只把 tone 语义色暴露给图标容器（color），
  图标容器透明底 —— 渐变底 + 白图标在组件内写不出来。
-->
<template>
  <div class="metric-card">
    <!-- 左图标槽：透明底 + tone 语义色，图标由调用方通过 #icon slot 传 Element Plus 图标 -->
    <div class="metric-icon" :style="iconColorStyle">
      <slot name="icon" />
    </div>

    <!-- 右内容区：标签 - 主值 - 辅助槽（辅助槽永远占位，保证同排卡等高） -->
    <div class="metric-info">
      <p class="metric-label">{{ label }}</p>
      <p class="metric-value">
        {{ value }}<span v-if="unit" class="metric-unit">{{ unit }}</span>
      </p>
      <div class="metric-aux">
        <p v-if="subText" class="metric-sub">{{ subText }}</p>
        <el-progress
          v-if="progress !== undefined"
          :percentage="progress"
          :stroke-width="6"
          :show-text="false"
        />
        <span
          v-if="direction"
          class="metric-direction"
          :style="{ color: directionColor }"
        >{{ directionLabel }}</span>
        <!-- 辅助槽永远占位：无任何辅助内容时渲染等高 invisible 占位行，同排卡片底边对齐 -->
        <span
          v-if="!subText && progress === undefined && !direction"
          class="metric-aux-placeholder"
          aria-hidden="true"
        >&nbsp;</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  /** 指标标签（中文标签 word-break: keep-all 防逐字竖排） */
  label: string
  /** 主值 */
  value: string | number
  /** 单位（如 % / V / Ah / A） */
  unit?: string
  /** 图标语义色；danger 只在真实告警时显式传 */
  tone?: 'success' | 'primary' | 'warning' | 'danger' | 'info'
  /** 副文本（如 "/ 105Ah"） */
  subText?: string
  /** 进度条 0-100（如 SOC），渲染 el-progress stroke-width=6 无文字 */
  progress?: number
  /** 方向指示：charge/discharge/idle。按少爷拍板：放电红、充电绿（语义方向色，非告警） */
  direction?: 'charge' | 'discharge' | 'idle' | null
}>(), {
  tone: 'primary',
  direction: null,
})

/**
 * 防回退：图标容器只暴露 tone 语义色（color），不暴露任何 background，
 * 旧「渐变底 + 白图标」风格在组件内无法写出。
 */
const iconColorStyle = computed(() => ({ color: `var(--el-color-${props.tone})` }))

const DIRECTION_LABELS: Record<'charge' | 'discharge' | 'idle', string> = {
  charge: '充电中',
  discharge: '放电中',
  idle: '静止',
}

const directionLabel = computed(() =>
  props.direction ? DIRECTION_LABELS[props.direction] : '',
)

/** 方向指示色（少爷拍板）：放电=danger红、充电=success绿、静止=中性；此处红/绿表方向不表告警 */
const directionColor = computed(() => {
  if (props.direction === 'charge') return 'var(--el-color-success)'
  if (props.direction === 'discharge') return 'var(--el-color-danger)'
  return 'var(--el-text-color-secondary)'
})
</script>

<style scoped>
.metric-card {
  background: var(--card-bg);
  border: 1px solid var(--el-border-color);
  border-radius: 12px;
  padding: 20px;
  min-height: 110px;
  display: flex;
  align-items: center;
  gap: 16px;
  transition: transform 0.3s, box-shadow 0.3s;
}
.metric-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
}

/* 左图标槽：透明底彩色图标 —— 只由 tone 内联色控制 color，无 background 声明 */
.metric-icon {
  width: 56px;
  height: 56px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  font-size: 28px;
}
.metric-icon :deep(.el-icon) {
  font-size: 28px;
}

/* 右内容区：标签 - 主值 - 辅助槽 */
.metric-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  justify-content: flex-start;
}
.metric-label {
  margin: 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
  /* 中文标签防止逐字断行竖排 */
  word-break: keep-all;
  overflow-wrap: break-word;
}
.metric-value {
  margin: 0;
  font-size: 22px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  line-height: 1.2;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.metric-unit {
  font-size: 15px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
  margin-left: 2px;
}

/* 辅助槽：min-height 保证永远占位，同排卡片底边对齐 */
.metric-aux {
  margin-top: 4px;
  min-height: 18px;
  font-size: 12px;
  line-height: 18px;
  color: var(--el-text-color-secondary);
}
.metric-sub {
  margin: 0;
  font-size: 12px;
  line-height: 18px;
}
.metric-direction {
  font-size: 12px;
  line-height: 18px;
}
.metric-aux-placeholder {
  visibility: hidden;
  line-height: 18px;
}

/* 移动端紧凑：图标 34px / 圆角 8px / 图标字号 22px，数值 20px，单位 13px */
@media (max-width: 768px) {
  .metric-card {
    border-radius: 10px;
    padding: 8px 4px;
    min-height: 64px;
    gap: 10px;
  }
  .metric-icon {
    width: 34px;
    height: 34px;
    border-radius: 8px;
    font-size: 22px;
  }
  .metric-icon :deep(.el-icon) {
    font-size: 22px;
  }
  .metric-value {
    font-size: 20px;
  }
  .metric-unit {
    font-size: 13px;
  }
}
</style>
