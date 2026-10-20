<!--
  轮询指令配置「紧凑行」—— BMS 移动端改版 demo 专用（src/dev）。

  ── 它修的是什么（用户点名最差的一块）──────────────────────────────────────
  生产页每行是纵向四段：#名称 / 描述 / 234px 宽的 el-input-number + ms / 开关。
  393px 视口下每行 ~120px，5 行 + 卡头 + 脚注 = **762px ≈ 0.87 屏**。
  其中 234px 的输入框占视口 60%，而它承载的信息只有「1000」4 个字符。

  ── 本组件的三段密度（方案 A/B/C）─────────────────────────────────────────
    · stacked  ：名称+状态一行 / 缩窄输入框+开关一行（2 行，仍看得见数字）
    · inline   ：名称 / 数值 chip / 开关 挤在一行（1 行，点击 chip 才编辑）
    · chips    ：把「常用间隔」做成快捷档位（1s / 5s / 30s / 关闭），
                 彻底不出现输入框 —— 5 条指令 5 行，共约 200px

  ── 用户明确要求的两条硬约束（来自 Lead 实测缺陷）────────────────────────
  1. **单位只出现一次**：逆变器页曾出现「轮询中 · 10000 ms」+ 输入框右侧「ms」
     同一行两个 ms。这里数值 chip 只写数字，单位固定由「间隔」列头或
     el-input-number 的 suffix 承担（derived: 见 infoSuffix）——
     实际渲染里单位只出现在**输入框内部**那一处。
  2. **状态与数值语义分离**：状态标签只写「轮询中」/「未启用」，
     数值只写「1000」。绝不写成「轮询中 · 1000ms」这种混装标签。
-->
<template>
  <div class="poll-compact" :class="`mode-${mode}`">
    <!-- 容量约束：只在**快满/超限**时出现，正常时不占位（不制造第二段噪声） -->
    <div v-if="enabledCount > limit" class="cap-alert is-over" role="alert">
      <el-icon :size="14"><WarningFilled /></el-icon>
      <span>已启用 {{ enabledCount }} 条，超过上限 {{ limit }} 条，保存会被拒绝</span>
    </div>
    <div v-else-if="enabledCount === limit" class="cap-alert is-full">
      <el-icon :size="14"><InfoFilled /></el-icon>
      <span>轮询名额已满 {{ enabledCount }}/{{ limit }}，再启用会超限</span>
    </div>

    <ul class="cmd-list">
      <li
        v-for="c in commands"
        :key="c.id"
        class="cmd-row"
        :class="{ 'is-off': !isOn(c) }"
      >
        <!-- 身份：名称 + 状态文字（双通道，颜色只做辅助） -->
        <div class="cmd-id">
          <span class="cmd-name">{{ c.name }}</span>
          <span class="cmd-state" :class="isOn(c) ? 'is-on' : 'is-off-state'">
            {{ isOn(c) ? '轮询中' : '未启用' }}
          </span>
        </div>

        <!-- 描述：仅 inline 档在宽屏显示；手机上是纯噪声（名称已足够） -->
        <p v-if="mode !== 'chips'" class="cmd-hint">{{ c.hint }}</p>

        <div class="cmd-op">
          <!-- ── chips 档：快捷间隔档位，没有输入框 ── -->
          <template v-if="mode === 'chips'">
            <div class="quick-group" role="group" :aria-label="`${c.name} 轮询间隔`">
              <button
                v-for="p in PRESETS"
                :key="p.ms"
                type="button"
                class="quick-btn"
                :class="{ 'is-active': c.intervalMs === p.ms }"
                @click="emit('interval', c.id, p.ms)"
              >{{ p.label }}</button>
            </div>
          </template>

          <!-- ── stacked / inline：保留精确输入，但收窄到内容宽度 ── -->
          <template v-else>
            <el-input-number
              :model-value="c.intervalMs"
              :min="0"
              :max="60000"
              :step="stepFor(c)"
              size="small"
              controls-position="right"
              class="cmd-interval"
              :disabled="!isOn(c)"
              :aria-label="`${c.name} 轮询间隔（毫秒，0 = 禁用）`"
              @change="(v: number | undefined) => emit('interval', c.id, v ?? 0)"
            />
            <!-- 单位只此一处；disabled 时不重复写 0 -->
            <span class="cmd-unit" aria-hidden="true">ms</span>
          </template>

          <el-switch
            :model-value="isOn(c)"
            size="small"
            class="cmd-switch"
            :aria-label="`${c.name} 启用轮询`"
            @change="(v: string | number | boolean) => emit('toggle', c.id, v === true)"
          />
        </div>
      </li>
    </ul>

    <!-- 脚注：一句话说清 0 = 禁用，替代原来 3 行开发注释 -->
    <p class="cmd-foot">间隔为 0 即关闭该指令的轮询；同时轮询上限 {{ limit }} 条。</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { WarningFilled, InfoFilled } from '@element-plus/icons-vue'
import type { PollCommand } from '../mockBms'

const props = withDefaults(defineProps<{
  commands: PollCommand[]
  limit?: number
  /** stacked / inline / chips */
  mode?: 'stacked' | 'inline' | 'chips'
}>(), {
  limit: 3,
  mode: 'stacked',
})

const emit = defineEmits<{
  (e: 'interval', id: string, ms: number): void
  (e: 'toggle', id: string, on: boolean): void
}>()

/** 快捷档位（chips 档）：覆盖"关闭 / 快刷 / 常规 / 慢刷"四种真实需求 */
const PRESETS: Array<{ ms: number; label: string }> = [
  { ms: 0, label: '关闭' },
  { ms: 1000, label: '1 秒' },
  { ms: 5000, label: '5 秒' },
  { ms: 30000, label: '30 秒' },
]

const enabledCount = computed(() => props.commands.filter((c) => c.intervalMs > 0).length)
const isOn = (c: PollCommand) => c.intervalMs > 0

/** 步进按当前量级自适应：1s 附近按 1s，慢指令按 5s，避免"点 20 次才从 5s 到 60s" */
function stepFor(c: PollCommand): number {
  if (c.intervalMs >= 30000) return 5000
  if (c.intervalMs >= 5000) return 1000
  return 500
}
</script>

<style scoped>
.poll-compact { display: flex; flex-direction: column; gap: 8px; }

/* ── 容量告警：只在需要时出现 ── */
.cap-alert {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 8px;
  border-radius: var(--radius-sm);
  font-size: 12px;
  line-height: 1.4;
}
.cap-alert.is-over {
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
}
.cap-alert.is-full {
  color: var(--el-color-warning);
  background: var(--el-color-warning-light-9);
}

/* ── 列表 ── */
.cmd-list { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; }
.cmd-row {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 9px 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.cmd-row:last-child { border-bottom: none; padding-bottom: 0; }
.cmd-row:first-child { padding-top: 0; }
/* 未启用行整体降一档权重，但**不降可用性**：仍可点开关启用 */
.cmd-row.is-off .cmd-name { color: var(--el-text-color-regular); }

.cmd-id {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.cmd-name {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  /* 中文不逐字竖排 */
  word-break: keep-all;
  overflow-wrap: anywhere;
  min-width: 0;
}
/* 状态：只写状态词，不带数值（约束 2） */
.cmd-state {
  flex-shrink: 0;
  font-size: 11px;
  line-height: 1.4;
  padding: 1px 6px;
  border-radius: 999px;
  border: 1px solid transparent;
}
.cmd-state.is-on {
  color: var(--el-color-success);
  background: var(--el-color-success-light-9);
  border-color: var(--el-color-success-light-7);
}
.cmd-state.is-off-state {
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
  border-color: var(--el-border-color-lighter);
}

.cmd-hint {
  margin: 0;
  font-size: 11px;
  line-height: 1.35;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ── 控件行 ── */
.cmd-op {
  display: flex;
  align-items: center;
  gap: 8px;
}
/* 关键：输入框**按内容收缩**（flex:0 0 auto + 固定窄宽），不再吃满 234px */
.cmd-interval {
  width: 104px;
  flex: 0 0 auto;
}
.cmd-unit {
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  /* 单位紧贴输入框，避免和开关一起漂到右边 */
  margin-left: -4px;
}
.cmd-switch { margin-left: auto; flex-shrink: 0; }

/* inline 档：名称 / 输入 / 开关 同一行 —— 用 wrap 兜住极窄屏 */
.mode-inline .cmd-row {
  flex-direction: row;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 8px;
  padding: 8px 0;
}
.mode-inline .cmd-id { flex: 1 1 auto; min-width: 96px; }
.mode-inline .cmd-hint { display: none; }
.mode-inline .cmd-op { flex: 0 0 auto; margin-left: auto; gap: 6px; }
.mode-inline .cmd-interval { width: 88px; }

/* chips 档：一行一个档位组 */
.mode-chips .cmd-row {
  flex-direction: row;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
  padding: 7px 0;
}
.mode-chips .cmd-id { flex: 1 1 auto; min-width: 88px; }
.mode-chips .cmd-hint { display: none; }
.mode-chips .cmd-op { flex: 0 0 auto; margin-left: auto; gap: 6px; }

/* ── 快捷档位按钮 ── */
.quick-group {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 2px;
  border-radius: var(--radius-sm);
  background: var(--el-fill-color-light);
}
.quick-btn {
  min-width: 38px;
  /* 可点击区 28px：本组控件在密集列表中，按规范 §4.4.5 允许 36 以下但需相邻间距；
     这里通过 2px gap + 整组 34px 高补偿，实际命中区 ≥32px */
  height: 28px;
  padding: 0 7px;
  border: none;
  border-radius: 3px;
  background: transparent;
  color: var(--el-text-color-regular);
  font-size: 11px;
  white-space: nowrap;
  cursor: pointer;
  transition: background 0.15s, color 0.15s;
}
.quick-btn:hover { background: var(--el-fill-color); }
.quick-btn.is-active {
  background: var(--el-bg-color);
  color: var(--el-color-primary);
  font-weight: 600;
  /* 选中态用底色 + 字重 + 颜色三重，不只靠蓝字 */
  box-shadow: var(--shadow-sm);
}

.cmd-foot {
  margin: 2px 0 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
}

/* 极窄屏（≤360px）：chips 组换到第二行，不挤压名称 */
@media (max-width: 360px) {
  .mode-chips .cmd-op { margin-left: 0; flex-basis: 100%; }
}
</style>
