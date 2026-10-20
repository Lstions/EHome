<!--
  MOS 直控 —— BMS 移动端改版 demo 专用（src/dev）。

  ── 它修的是什么 ──────────────────────────────────────────────────────────
  生产页刚控卡把两段开发说明（「充电 / 放电回路通断的软件策略 set_mos_policy」、
  「着色规则：导通用成功色、关断用中性色 —— 关断是正常工况，不是故障」）
  当正文渲染。用户原话「UI 设计不规范」，这些是给开发者看的，
  不该出现在设备运维界面上。这里压缩成一行提示，并且在 sheet 档完全收进 tooltip。

  ── 三个档位 ──────────────────────────────────────────────────────────────
    · row   每行：图标 / 名称 + 状态 / 开关（44px 行高）
    · grid  两个方块并排，各自一个大开关 —— 更像"电灯开关"（用户原话）
    · strip 名称 + 状态 + 开关 全部压进一行两个 cell，与指标带同一节奏
-->
<template>
  <div class="mos" :class="`v-${variant}`">
    <!--
      标题归属：默认由本组件自带（方案 A/C 直接嵌入"运行状态"卡，没有外层卡头）；
      外层已有卡头时传 :show-title="false"，否则会出现两级同名标题
      （方案 B 初版就是「MOS 直控」上下各一个，评审实测已复现）。
    -->
    <div v-if="showTitle" class="mos-head">
      <span class="mos-title">MOS 直控</span>
      <span class="mos-fetch">fet_status {{ fetHex }}</span>
    </div>
    <div v-else class="mos-fetch-row">
      <span class="mos-fetch">fet_status {{ fetHex }}</span>
    </div>

    <div class="mos-list">
      <div v-for="m in switches" :key="m.key" class="mos-item" :class="{ 'is-off': !m.on }">
        <span class="mos-gate" :class="m.on ? 'is-on' : 'is-off'" aria-hidden="true">
          <el-icon :size="variant === 'grid' ? 18 : 15">
            <Open v-if="m.on" /><Lock v-else />
          </el-icon>
        </span>
        <div class="mos-main">
          <div class="mos-name-line">
            <span class="mos-name">{{ m.name }}</span>
            <span class="mos-state" :class="m.on ? 'is-on' : 'is-off-state'">
              {{ m.on ? '导通' : '关断' }}
            </span>
          </div>
          <span v-if="variant !== 'strip'" class="mos-desc">{{ m.desc }} · {{ m.bit }}</span>
        </div>
        <el-switch
          :model-value="m.on"
          size="small"
          class="mos-switch"
          :aria-label="`${m.name}（${m.desc}）`"
          @change="(v: string | number | boolean) => emit('toggle', m.key, v === true)"
        />
      </div>
    </div>

    <!-- 提示：一行、可读；不再渲染整段开发说明 -->
    <p v-if="variant !== 'strip'" class="mos-note">
      切换需一次确认（回读 fet_status 校验）。关断是正常工况，不是故障。
    </p>
  </div>
</template>

<script setup lang="ts">
import { Open, Lock } from '@element-plus/icons-vue'
import type { MosSwitch } from '../mockBms'

withDefaults(defineProps<{
  switches: MosSwitch[]
  fetHex?: string
  /** row / grid / strip */
  variant?: 'row' | 'grid' | 'strip'
  /** 外层卡片是否已有标题（有则不重复渲染） */
  showTitle?: boolean
}>(), {
  fetHex: '0x03',
  variant: 'row',
  showTitle: true,
})

const emit = defineEmits<{
  (e: 'toggle', key: 'charge' | 'discharge', on: boolean): void
}>()
</script>

<style scoped>
.mos { display: flex; flex-direction: column; gap: 8px; }

.mos-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.mos-title { font-size: 13px; font-weight: 600; color: var(--el-text-color-primary); }
/* 无标题模式：只在右上角保留 fet_status 辅助值 */
.mos-fetch-row {
  display: flex;
  justify-content: flex-end;
  margin-bottom: -2px;
}
.mos-fetch {
  margin-left: auto;
  font-family: 'SF Mono', Consolas, Monaco, monospace;
  font-size: 10px;
  color: var(--el-text-color-secondary);
}

.mos-list { display: flex; flex-direction: column; }
.mos-item {
  display: flex;
  align-items: center;
  gap: 9px;
  min-height: 44px;
  padding: 6px 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
  min-width: 0;
}
.mos-item:last-child { border-bottom: none; padding-bottom: 2px; }

.mos-gate {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  border-radius: var(--radius-sm);
  background: var(--el-fill-color-light);
}
.mos-gate.is-on { color: var(--el-color-success); }
.mos-gate.is-off { color: var(--el-text-color-secondary); }

.mos-main { flex: 1; min-width: 0; }
.mos-name-line { display: flex; align-items: center; gap: 6px; min-width: 0; }
.mos-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  word-break: keep-all;
  white-space: nowrap;
}
/* 状态：文字 + 颜色双通道；导通绿、关断中性灰（关断不是故障，不用 danger） */
.mos-state {
  flex-shrink: 0;
  padding: 0 6px;
  border-radius: 999px;
  font-size: 10px;
  line-height: 1.6;
}
.mos-state.is-on {
  color: var(--el-color-success);
  background: var(--el-color-success-light-9);
}
.mos-state.is-off-state {
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
}
.mos-desc {
  display: block;
  margin-top: 1px;
  font-size: 10px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.mos-switch { flex-shrink: 0; }

.mos-note {
  margin: 0;
  font-size: 10px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
}

/* ── grid：两个方块并排，开关放大成"电灯"形态 ── */
.v-grid .mos-list {
  flex-direction: row;
  gap: 8px;
}
.v-grid .mos-item {
  flex: 1 1 0;
  min-width: 0;
  flex-direction: column;
  align-items: flex-start;
  gap: 6px;
  min-height: 0;
  padding: 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--radius-md);
  background: var(--el-fill-color-lighter);
}
.v-grid .mos-item:last-child { padding-bottom: 10px; border-bottom: 1px solid var(--el-border-color-lighter); }
.v-grid .mos-gate { width: 34px; height: 34px; }
.v-grid .mos-main { width: 100%; }
.v-grid .mos-name { font-size: 13px; }
/* 开关在方块里独占一行右对齐：手指够得到，也不会挤压名称 */
.v-grid .mos-switch {
  align-self: flex-end;
  margin-top: auto;
  transform: scale(1.15);
  transform-origin: right center;
}

/* ── strip：两格并排，与指标带同节奏 ── */
.v-strip .mos-list { flex-direction: row; gap: 8px; }
.v-strip .mos-item {
  flex: 1 1 0;
  min-width: 0;
  min-height: 36px;
  padding: 4px 0;
  border-bottom: none;
  gap: 6px;
}
.v-strip .mos-gate { width: 24px; height: 24px; border-radius: 3px; }
.v-strip .mos-name { font-size: 12px; }
.v-strip .mos-note { display: none; }
</style>
