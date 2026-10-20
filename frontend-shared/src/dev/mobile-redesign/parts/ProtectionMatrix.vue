<!--
  保护状态「状态矩阵」—— BMS 移动端改版 demo 专用（src/dev）。

  ── 它替代什么 ──────────────────────────────────────────────────────────────
  生产页在手机上把 12 项保护渲染成 12 行「名称 + 正常标签 + bitN」，
  实测单列 295px、总高约 552px，占掉 0.63 屏，而用户要判断的只有一件事：
  「有没有异常」。12 行重复的"正常"是 11 行冗余信息。

  ── 三种密度（按方案档位）──────────────────────────────────────────────────
    · cells-3  3 列 × 4 行，名称 + 状态文字，最保守（不隐藏任何字样）
    · cells-4  4 列 × 3 行，名称 + 圆点，省一行高度
    · heat     12 格色块一行（每格 24px），全部正常时只剩一行结论；
               有触发项时把**触发项名字**提到结论行 ——
               名称只在需要时出现，而不是 12 行常驻。

  ── 状态表达 ────────────────────────────────────────────────────────────────
  必须"文字 + 颜色"双通道（规范 §4.2.2）：
    · 正常态用 success 色的 ● + 文字「正常」
    · 触发态用 danger 色的 ● + 文字「触发」
  色块本身不是唯一通道：heat 档每格有 aria-label，且结论行用文字点名触发项。
-->
<template>
  <div class="protect" :class="`protect--${mode}`">
    <!-- 结论行：12 项全绿时这是整块唯一的句子；有触发项时点名是哪几项 -->
    <div class="protect-summary" :class="anyActive ? 'is-alert' : 'is-ok'">
      <el-icon :size="15">
        <WarningFilled v-if="anyActive" />
        <CircleCheckFilled v-else />
      </el-icon>
      <span class="summary-text">{{ summaryText }}</span>
      <!-- 位掩码是运维核对用的辅助信息，不与结论抢视觉（规范 §4.2.4） -->
      <span class="summary-meta">bitmask {{ statusHex }}</span>
    </div>

    <!-- heat：一行 12 格 -->
    <div v-if="mode === 'heat'" class="heat-strip" role="list" aria-label="12 项保护状态">
      <span
        v-for="b in bits"
        :key="b.bit"
        class="heat-cell"
        :class="b.active ? 'is-active' : 'is-normal'"
        role="listitem"
        :aria-label="`bit${b.bit} ${b.label} ${b.active ? '已触发' : '正常'}`"
        :title="`${b.label}（bit${b.bit}）`"
      >
        <span class="heat-dot" aria-hidden="true"></span>
      </span>
    </div>

    <!-- cells：名称常驻网格 -->
    <div v-else class="cell-grid" :class="`cols-${columns}`" role="list">
      <div
        v-for="b in bits"
        :key="b.bit"
        class="cell-item"
        :class="{ 'is-active': b.active }"
        role="listitem"
      >
        <span class="cell-dot" aria-hidden="true"></span>
        <span class="cell-label">{{ b.label }}</span>
        <span class="cell-state">{{ b.active ? '触发' : '正常' }}</span>
      </div>
    </div>

    <!-- heat 档：明细按需展开（默认收起，因为"全绿"时明细零信息量） -->
    <button
      v-if="mode === 'heat' && showDetailToggle"
      type="button"
      class="detail-toggle"
      :aria-expanded="detailOpen"
      @click="detailOpen = !detailOpen"
    >
      <el-icon :size="12" class="caret" :class="{ open: detailOpen }"><ArrowRight /></el-icon>
      {{ detailOpen ? '收起 12 项明细' : '查看 12 项明细' }}
    </button>
    <div v-if="mode === 'heat' && detailOpen" class="cell-grid cols-3" role="list">
      <div
        v-for="b in bits"
        :key="'d' + b.bit"
        class="cell-item"
        :class="{ 'is-active': b.active }"
        role="listitem"
      >
        <span class="cell-dot" aria-hidden="true"></span>
        <span class="cell-label">{{ b.label }}</span>
        <span class="cell-state">{{ b.active ? '触发' : '正常' }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { CircleCheckFilled, WarningFilled, ArrowRight } from '@element-plus/icons-vue'
import type { ProtectionBit } from '../mockBms'

const props = withDefaults(defineProps<{
  bits: ProtectionBit[]
  /** cells-3 / cells-4 / heat */
  mode?: 'cells-3' | 'cells-4' | 'heat'
  statusHex?: string
  showDetailToggle?: boolean
}>(), {
  mode: 'cells-3',
  statusHex: '0x00',
  showDetailToggle: true,
})

const detailOpen = ref(false)

const columns = computed(() => (props.mode === 'cells-4' ? 4 : 3))
const anyActive = computed(() => props.bits.some((b) => b.active))

const summaryText = computed(() => {
  const hits = props.bits.filter((b) => b.active)
  if (!hits.length) return `${props.bits.length} 项保护均未触发`
  if (hits.length === 1) return `${hits[0].label} 已触发`
  return `${hits.length} 项已触发：${hits.map((h) => h.label).join('、')}`
})
</script>

<style scoped>
.protect { display: flex; flex-direction: column; gap: 8px; }

/* 结论行 */
.protect-summary {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 22px;
  font-size: 13px;
  font-weight: 500;
  line-height: 1.35;
}
.protect-summary.is-ok { color: var(--el-color-success); }
.protect-summary.is-alert { color: var(--el-color-danger); }
.summary-text { word-break: keep-all; overflow-wrap: anywhere; }
.summary-meta {
  margin-left: auto;
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 400;
  color: var(--el-text-color-secondary);
  font-family: 'SF Mono', Consolas, Monaco, monospace;
}

/* ── 网格（cells-3 / cells-4） ── */
.cell-grid {
  display: grid;
  gap: 6px;
  /* 行高统一：名称长短不一（「短路保护」vs「前端 IC 错误」）不能把行撑歪 */
  grid-auto-rows: 1fr;
}
.cell-grid.cols-3 { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.cell-grid.cols-4 { grid-template-columns: repeat(4, minmax(0, 1fr)); }

.cell-item {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  min-height: 34px;
  padding: 6px 8px;
  border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter);
  /* 正常项不使用任何语义色填充：绿色只落在圆点与状态文字上，避免整块变绿噪 */
  border: 1px solid transparent;
  font-size: 11px;
  line-height: 1.25;
}
.cell-item.is-active {
  background: var(--el-color-danger-light-9);
  border-color: var(--el-color-danger-light-5);
}
.cell-dot {
  flex-shrink: 0;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--el-color-success);
}
.cell-item.is-active .cell-dot { background: var(--el-color-danger); }
.cell-label {
  flex: 1;
  min-width: 0;
  color: var(--el-text-color-regular);
  /* 中文不逐字竖排（规范 §4.2.5）；允许整词换行 */
  word-break: keep-all;
  overflow-wrap: anywhere;
}
/*
  状态紧跟名称而不是被推到格子最右：
  3 列时格子约 110px，「单体过压」占 ~55px，若状态右对齐会与名称隔开 40px，
  读起来像两列不相干的信息。状态是**名称的谓词**，必须相邻。
*/
.cell-state {
  flex-shrink: 0;
  color: var(--el-color-success);
  font-size: 10px;
}
.cell-item.is-active .cell-state { color: var(--el-color-danger); }
/* 4 列时把状态文字收进圆点（宽度不够），状态由圆点色 + aria-label + 结论行承担 */
.cols-4 .cell-state { display: none; }
.cols-4 .cell-item { justify-content: flex-start; }

/* ── heat 条（12 格一行） ── */
.heat-strip {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: 3px;
}
.heat-cell {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 26px;
  border-radius: 3px;
  background: var(--el-color-success-light-8);
}
.heat-cell.is-active {
  background: var(--el-color-danger);
  /* 触发格是唯一的实心块 —— 在 12 格里它必须跳出来 */
  box-shadow: 0 0 0 2px var(--el-color-danger-light-7);
}
.heat-dot {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--el-color-success);
}
.heat-cell.is-active .heat-dot { background: #fff; }

/* 明细展开 */
.detail-toggle {
  align-self: flex-start;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-height: 32px;
  padding: 0;
  border: none;
  background: none;
  color: var(--el-color-primary);
  font-size: 12px;
  cursor: pointer;
}
.caret { transition: transform 0.15s; }
.caret.open { transform: rotate(90deg); }
</style>
