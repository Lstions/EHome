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
      <!--
        辅助槽：内容自适应多行，卡片高度由同一行的**最高**卡片决定（见 CSS 的
        .metric-card 说明）。

        演进史（两版都被实测证伪，记下来防回退）：
          v1  min-height:18px —— 只兜住"有没有副文本"，没兜住 progress(6px)
              与 direction 第二行 ⇒ SOC 卡高 5px、逆变器电池卡高 17px；
          v2  height:18px + overflow:hidden + progress 绝对定位 —— 等高做到了，
              但把多行副文本**硬裁**掉（实测 1024px 下电池卡裁 70px、BMS 卡裁 34px），
              且进度条与文本实交 4px。这是"用信息丢失换等高"，不可接受。
          v3（当前）槽体不设固定高度、不裁切；等高改由外层 .row 的 stretch +
              卡片 flex 布局保证（见各页 CSS）。信息完整优先于形态整齐。
      -->
      <div class="metric-aux">
        <div class="metric-aux-line">
          <p v-if="subText" class="metric-sub">{{ subText }}</p>
          <span
            v-if="direction"
            class="metric-direction"
            :style="{ color: directionColor }"
          >{{ directionLabel }}</span>
        </div>
        <el-progress
          v-if="progress !== undefined"
          class="metric-progress"
          :percentage="progress"
          :stroke-width="6"
          :show-text="false"
        />
        <!-- 辅助槽永远占位：无任何辅助内容时渲染等高 invisible 占位行 -->
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
  /*
    同排等高靠外层 .row 的 align-items:stretch 实现（本组件不再用固定高度兜）。
    高度被拉伸时，让内容在垂直方向居中而不是吊在顶端 —— 否则矮内容卡片
    底部会出现一片空白，视觉上像"卡片没对齐"。
  */
  box-sizing: border-box;
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
/*
  内容区纵向 flex。justify-content 用 center 而不是 flex-start：
  同排等高由外层 .row 的 stretch 实现后，矮内容卡片会被拉高，
  若内容吊在顶端，底部会留下一片空白，视觉上像"没对齐"。
*/
.metric-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  justify-content: center;
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

/*
  辅助槽：**恒定高度预算**（固定 height，不是 min-height）。
  为什么必须是 height 而不是 min-height：
    min-height 只能兜住"内容更少"的情况；一旦某张卡多出 progress(6px) 或
    第二行 direction，min-height 会被内容顶开 —— 这正是实测 115 vs 110、
    127 vs 110 的来源。用固定 height + 内部 overflow 收敛，任何组合都同高。
  direction 与 subText 走同一行（.metric-aux-line 用 flex），因此不会占第二行。
*/
/*
  辅助槽：**不设固定高度、不裁切**（v3，2026-10-20 二次复审后定稿）。
  v2 的 height:18px + overflow:hidden 能等高，但把多行副文本硬裁掉
  （实测 1024px：逆变器电池卡裁 70px、BMS 四卡裁 16~34px）——用信息丢失换整齐，
  不可接受。现在槽体随内容增高，同排等高改由外层 .row 的 align-items:stretch
  + 卡片自身 flex 布局保证（见 BmsDetailPage / InverterDetailPage）。
*/
.metric-aux {
  margin-top: 4px;
  font-size: 12px;
  line-height: 18px;
  color: var(--el-text-color-secondary);
}
.metric-aux-line {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}
/*
  副文本允许折行而不是省略号：
  移动端实测卡片内容区仅 97px，「健康区间 20% – 90%」「充电中（电流为正）」
  在 nowrap+ellipsis 下会被截成「健康区间 20% –…」—— 同样是"用信息丢失换行齐"。
  槽体高度固定 18px 只够一行，因此窄屏把槽体高度放宽到两行（见媒体查询），
  让文字完整可读且同排仍然等高。
*/
.metric-sub {
  margin: 0;
  font-size: 12px;
  line-height: 18px;
  min-width: 0;
  overflow-wrap: anywhere;
}
.metric-direction {
  font-size: 12px;
  line-height: 18px;
  flex-shrink: 0;
  white-space: nowrap;
}
.metric-aux-placeholder {
  visibility: hidden;
  line-height: 18px;
}
/*
  progress 回到正常文档流（v2 曾用绝对定位压到槽底，实测与副文本行盒相交 4px，
  390px 下蓝色进度条正压在「90%」上）。现在作为独立一行排在副文本下方，
  margin-top 给 2px 视觉间隙；卡片总高增加 6px，由 stretch 保证同排仍等高。
*/
.metric-progress {
  margin-top: 2px;
  line-height: 0;
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
  /* 窄屏副文本折行：允许换行，槽体自增高，不再用固定 36px 硬裁 */
  .metric-aux-line {
    flex-wrap: wrap;
    gap: 0 6px;
  }
}
</style>
