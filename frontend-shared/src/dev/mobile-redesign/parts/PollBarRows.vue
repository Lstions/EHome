<!--
  轮询指令「条形行」—— 把"图表"与"编辑器"合成一个控件（方案 A / C 使用）。

  ── 为什么不再分两张（图 + 列表）─────────────────────────────────────────
  方案初版是「ECharts 间隔条形图」+「指令列表」上下两块，评审时发现一个硬伤：
  **同一组指令名出现两次**。用户要在上图读"谁刷得快"，又要到下图找同一行改间隔，
  两次定位成本，而且两张卡加起来 460px —— 比生产页的 762px 好，但还不够。

  合并后每一行同时承担三件事：
    ① 身份：名称 + 状态（文字 + 圆点双通道）
    ② 量：条形长度 = 间隔秒数（同一行内直接比较快慢）
    ③ 改：右侧快捷档位，点一下即改（不弹窗、不出现输入框）

  ── 单位只出现一次（Lead 实测缺陷约束）───────────────────────────────────
  单位固定写在**行尾**（"5 秒"由档位标签本身带出），条形不带单位、状态标签不带数值。
  绝不出现「轮询中 · 5000 ms」+ 输入框右侧再一个 ms 的同行双单位。

  ── 交互 ──────────────────────────────────────────────────────────────────
  · 点档位按钮 = 直接设为该间隔；「关闭」= 置 0
  · 开关 = 启用/禁用（启用时恢复该指令的模板默认间隔）
  两者语义不同但都可达，且状态永远以文字写出来。
-->
<template>
  <div class="poll-bars">
    <!-- 容量约束：只在快满/超限时出现，正常时不占位 -->
    <div v-if="enabledCount > limit" class="cap is-over" role="alert">
      <el-icon :size="13"><WarningFilled /></el-icon>
      <span>已启用 {{ enabledCount }} 条，超过上限 {{ limit }} 条，保存会被拒绝</span>
    </div>
    <div v-else-if="enabledCount === limit" class="cap is-full">
      <el-icon :size="13"><InfoFilled /></el-icon>
      <span>名额已用满 {{ enabledCount }}/{{ limit }}</span>
    </div>

    <ul class="rows">
      <li v-for="c in commands" :key="c.id" class="row" :class="{ 'is-off': !isOn(c) }">
        <!-- ① 身份 + ② 量：名称、状态、条形同处一行 -->
        <div class="row-line">
          <span class="name">{{ c.name }}</span>
          <span class="state" :class="isOn(c) ? 'is-on' : 'is-off-state'">
            {{ isOn(c) ? '轮询中' : '未启用' }}
          </span>
          <!-- 条形：宽度 = 间隔 / 60s 满刻度。禁用时只剩一条浅色轨道 -->
          <span class="bar" aria-hidden="true">
            <span class="bar-fill" :style="{ width: barWidth(c) }"></span>
          </span>
          <span class="val">{{ isOn(c) ? formatInterval(c.intervalMs) : '—' }}</span>
        </div>

        <!-- ③ 改：快捷档位 + 开关 -->
        <div class="row-op">
          <div class="presets" role="group" :aria-label="`${c.name} 轮询间隔`">
            <button
              v-for="p in PRESETS"
              :key="p.ms"
              type="button"
              class="preset"
              :class="{ 'is-active': c.intervalMs === p.ms }"
              :aria-pressed="c.intervalMs === p.ms"
              @click="emit('interval', c.id, p.ms)"
            >{{ p.label }}</button>
          </div>
          <el-switch
            :model-value="isOn(c)"
            size="small"
            class="sw"
            :aria-label="`${c.name} 启用轮询`"
            @change="(v: string | number | boolean) => emit('toggle', c.id, v === true)"
          />
        </div>
      </li>
    </ul>

    <p class="foot">间隔为 0 即关闭该指令；同时轮询上限 {{ limit }} 条。</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { WarningFilled, InfoFilled } from '@element-plus/icons-vue'
import { formatInterval, type PollCommand } from '../mockBms'

const props = withDefaults(defineProps<{
  commands: PollCommand[]
  limit?: number
}>(), {
  limit: 3,
})

const emit = defineEmits<{
  (e: 'interval', id: string, ms: number): void
  (e: 'toggle', id: string, on: boolean): void
}>()

/** 快捷档位：覆盖"关闭 / 快刷 / 常规 / 慢刷"四种真实需求 */
const PRESETS: Array<{ ms: number; label: string }> = [
  { ms: 0, label: '关' },
  { ms: 1000, label: '1s' },
  { ms: 5000, label: '5s' },
  { ms: 30000, label: '30s' },
]

/** 条形满刻度 60s（后端上限），与档位一致；不写死像素宽度，用百分比 */
const BAR_FULL_MS = 60000

const enabledCount = computed(() => props.commands.filter((c) => c.intervalMs > 0).length)
const isOn = (c: PollCommand) => c.intervalMs > 0

function barWidth(c: PollCommand): string {
  if (c.intervalMs <= 0) return '0%'
  // 最小 8%：1s 在 60s 满刻度下只有 1.7%，看起来像没启用 —— 一个"下限可见"的长度
  return Math.max(8, Math.min((c.intervalMs / BAR_FULL_MS) * 100, 100)) + '%'
}
</script>

<style scoped>
.poll-bars { display: flex; flex-direction: column; gap: 6px; }

.cap {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 5px 7px;
  border-radius: var(--radius-sm);
  font-size: 11px;
  line-height: 1.4;
}
.cap.is-over { color: var(--el-color-danger); background: var(--el-color-danger-light-9); }
.cap.is-full { color: var(--el-color-warning); background: var(--el-color-warning-light-9); }

.rows { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; }
.row {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 7px 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.row:last-child { border-bottom: none; padding-bottom: 0; }
.row:first-child { padding-top: 0; }

/* 第一行：名称 / 状态 / 条形 / 数值 */
.row-line {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.name {
  flex-shrink: 0;
  max-width: 46%;
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  word-break: keep-all;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.row.is-off .name { color: var(--el-text-color-regular); font-weight: 400; }

/* 状态：只写状态词，不带数值（数值在右侧 .val 独立成列） */
.state {
  flex-shrink: 0;
  padding: 0 5px;
  border-radius: 999px;
  font-size: 10px;
  line-height: 1.6;
}
.state.is-on { color: var(--el-color-success); background: var(--el-color-success-light-9); }
.state.is-off-state { color: var(--el-text-color-secondary); background: var(--el-fill-color-light); }

/* 条形：吃掉剩余宽度，长度即间隔。轨道始终可见，空轨道也能读"这一条没启用" */
.bar {
  flex: 1 1 auto;
  min-width: 24px;
  height: 6px;
  border-radius: 3px;
  background: var(--el-fill-color);
  overflow: hidden;
}
.bar-fill {
  display: block;
  height: 100%;
  border-radius: 3px;
  background: var(--el-color-primary);
  transition: width 0.25s;
}
.row.is-off .bar-fill { background: transparent; }

/* 数值：单位只在此处出现（"1 秒"/"5 秒"由 formatInterval 带出） */
.val {
  flex-shrink: 0;
  min-width: 40px;
  text-align: right;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--el-text-color-regular);
}
.row.is-off .val { color: var(--el-text-color-placeholder); }

/* 第二行：档位 + 开关 */
.row-op {
  display: flex;
  align-items: center;
  gap: 6px;
}
.presets {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 2px;
  border-radius: var(--radius-sm);
  background: var(--el-fill-color-light);
}
.preset {
  min-width: 34px;
  height: 26px;
  padding: 0 6px;
  border: none;
  border-radius: 3px;
  background: transparent;
  color: var(--el-text-color-regular);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  cursor: pointer;
}
.preset:hover { background: var(--el-fill-color); }
.preset.is-active {
  background: var(--el-bg-color);
  color: var(--el-color-primary);
  font-weight: 600;
  box-shadow: var(--shadow-sm);
}
.sw { margin-left: auto; flex-shrink: 0; }

.foot {
  margin: 2px 0 0;
  font-size: 10px;
  line-height: 1.45;
  color: var(--el-text-color-secondary);
}
</style>
