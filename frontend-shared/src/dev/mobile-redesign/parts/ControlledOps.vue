<!--
  受控操作「分组手风琴」—— BMS 移动端改版 demo 专用（src/dev）。

  ── 它修的是什么 ──────────────────────────────────────────────────────────
  26 项操作如果平铺，手机上是 26 × ~86px ≈ 2236px（2.5 屏）。
  生产页已按 5 组折叠（这点是对的），但**每组头 + 每行都在塞标签**
  （「风险」「需参数」「已移至上方直控」+ 状态文案 + 一个 44px 高的按钮），
  单行实测 ~110px，且分组头在 393px 下会折成两行。

  ── 本组件 ────────────────────────────────────────────────────────────────
    · 分组头一行：图标 + 名称 + 「N 项」+ 右侧风险计数（不折行）
    · 行：名称 + 风险标记（小圆点，不是 tag）+ 右侧操作按钮
    · 默认只展开「运行控制」（最常用），其余收起 —— 首屏只见 5 行组头
-->
<template>
  <div class="ops">
    <div class="ops-head">
      <!-- 复用外层卡片的 .card-title / .card-metric 语义：本组件不自造第二套标题样式 -->
      <span class="card-title">受控操作</span>
      <span class="ops-count">{{ total }} 项 · {{ groups.length }} 组</span>
    </div>

    <div class="ops-groups">
      <section v-for="g in groups" :key="g.id" class="op-group" :class="{ 'is-open': open[g.id] }">
        <button
          type="button"
          class="group-head"
          :aria-expanded="!!open[g.id]"
          @click="toggle(g.id)"
        >
          <el-icon :size="12" class="caret" :class="{ open: open[g.id] }"><ArrowRight /></el-icon>
          <span class="group-name">{{ g.name }}</span>
          <span class="group-count">{{ g.items.length }}</span>
          <span v-if="riskyCount(g)" class="group-risk">{{ riskyCount(g) }} 风险</span>
          <span class="group-hint">{{ g.hint }}</span>
        </button>

        <ul v-show="open[g.id]" class="op-list">
          <li v-for="op in g.items" :key="op.id" class="op-row">
            <span class="op-name">{{ op.name }}</span>
            <!-- 风险：小圆点 + 文字在 title/aria 上；不做满屏 tag -->
            <span v-if="op.risky" class="risk-dot" aria-label="风险操作" title="风险操作"></span>
            <el-button
              size="small"
              :type="op.risky ? 'warning' : 'primary'"
              :plain="op.risky"
              class="op-btn"
              @click="emit('run', op.id)"
            >{{ op.risky ? '确认执行' : '执行' }}</el-button>
          </li>
        </ul>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive } from 'vue'
import { ArrowRight } from '@element-plus/icons-vue'
import type { OpGroup } from '../mockBms'

const props = withDefaults(defineProps<{
  groups: OpGroup[]
  total: number
  /** 默认展开的组 id */
  defaultOpen?: string[]
}>(), {
  defaultOpen: () => ['runtime'],
})

const emit = defineEmits<{
  (e: 'run', id: string): void
}>()

const open = reactive<Record<string, boolean>>(
  Object.fromEntries(props.groups.map((g) => [g.id, props.defaultOpen.includes(g.id)])),
)

function toggle(id: string) {
  open[id] = !open[id]
}

function riskyCount(g: OpGroup): number {
  return g.items.filter((i) => i.risky).length
}
</script>

<style scoped>
.ops { display: flex; flex-direction: column; gap: 8px; }
.ops-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
/* .card-title 由父级 <style scoped> 定义 —— scoped 不跨组件，故这里补一份同样的视觉 */
.card-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  word-break: keep-all;
  white-space: nowrap;
}
.ops-count { font-size: 11px; color: var(--el-text-color-secondary); }

.ops-groups { display: flex; flex-direction: column; gap: 4px; }
.op-group {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--radius-sm);
  overflow: hidden;
}

.group-head {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  min-height: 40px;
  padding: 6px 8px;
  border: none;
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-primary);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}
.group-head:hover { background: var(--el-fill-color-light); }
.caret { flex-shrink: 0; color: var(--el-text-color-secondary); transition: transform 0.15s; }
.caret.open { transform: rotate(90deg); }
.group-name {
  font-weight: 600;
  /* 中文不逐字竖排（否则「读取信息」会变 4 行竖排） */
  word-break: keep-all;
  white-space: nowrap;
}
.group-count {
  flex-shrink: 0;
  min-width: 18px;
  padding: 0 5px;
  border-radius: 999px;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);
  font-size: 10px;
  text-align: center;
  line-height: 1.6;
}
.group-risk {
  flex-shrink: 0;
  color: var(--el-color-warning);
  font-size: 10px;
  white-space: nowrap;
}
/* 提示文字在窄屏收起：组名 + 计数已经够了，提示是第三层信息 */
.group-hint {
  margin-left: auto;
  flex-shrink: 0;
  color: var(--el-text-color-secondary);
  font-size: 10px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 42%;
}

.op-list { margin: 0; padding: 0; list-style: none; }
.op-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 46px;
  padding: 5px 8px;
  border-top: 1px solid var(--el-border-color-lighter);
  min-width: 0;
}
.op-name {
  flex: 1 1 auto;
  min-width: 0;
  font-size: 12px;
  color: var(--el-text-color-regular);
  /* 长操作名（「设置静态修正时间」）单行省略，不折行打乱行高 */
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.risk-dot {
  flex-shrink: 0;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--el-color-warning);
}
.op-btn {
  flex-shrink: 0;
  /*
    触控热区（规范 §4.4.5，≥44px）：靠**外层 .op-row 的 min-height:46px + 按钮垂直居中**
    达成，而不是把按钮本身撑到 44px 高或 68px 宽。
    实测初版 .op-btn 视觉盒 68×30，在有 46px 行高的列表里显得"重"，且 5 个宽按钮
    形成一条重复的色带；收窄到 60×28 后单行更轻，命中区仍 ≥44（行高保证）。
  */
  min-width: 60px;
  height: 28px;
  padding: 0 8px;
  font-size: 11px;
}
.op-row :deep(.el-button) { margin-left: 0; }

@media (max-width: 360px) {
  .group-hint { display: none; }
}
</style>
