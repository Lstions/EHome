<template>
  <!-- 受控操作（目录来自后端，按用途分组） + 操作历史 -->
  <section class="row ops-history">
    <div class="card ops-card">
      <div class="card-head">
        <span class="card-title">受控操作</span>
        <span class="card-hint">共 {{ totalOps }} 项 · 按用途分组，默认收起</span>
        <span class="head-right state-legend">
          <el-icon :size="13"><InfoFilled /></el-icon>
          状态文案：已发送 → 执行中 → 校验中 → 成功 / 失败 / 结果未知 / 已取消
        </span>
      </div>

      <el-alert v-if="catalogError" type="error" :closable="false" show-icon class="inline-alert">
        <template #title>受控操作目录加载失败</template>
        <div class="alert-detail">
          {{ catalogError }}
          <el-button size="small" text type="primary" @click="loadOperations">重试</el-button>
        </div>
      </el-alert>
      <div v-else-if="catalogLoading" class="stream-empty">正在加载受控操作目录…</div>

      <div class="op-groups">
        <div v-for="g in opGroups" :key="g.id" class="op-group">
          <button type="button" class="op-group-head" @click="toggleGroup(g.id)">
            <el-icon :size="14" class="op-caret" :class="{ open: openGroups[g.id] }"><ArrowRight /></el-icon>
            <el-icon :size="15" class="op-group-icon"><component :is="g.icon" /></el-icon>
            <span class="op-group-name">{{ g.name }}</span>
            <span class="op-group-count">{{ g.ops.length }} 项</span>
            <el-tag v-if="riskCount(g)" type="danger" size="small" effect="plain">{{ riskCount(g) }} 项风险</el-tag>
            <span class="op-group-hint">{{ g.hint }}</span>
          </button>

          <div v-show="openGroups[g.id]" class="op-list">
            <div v-for="op in g.ops" :key="op.id" class="op-item">
              <div class="op-info">
                <div class="op-name-line">
                  <span class="op-name">{{ op.name }}</span>
                  <el-tag :type="op.risky ? 'danger' : 'info'" size="small" effect="plain">{{ op.risky ? '风险' : '普通' }}</el-tag>
                  <el-tag v-if="!op.available" type="warning" size="small" effect="plain">暂不可用</el-tag>
                  <el-tag v-if="op.needsParams" type="info" size="small" effect="plain">需参数</el-tag>
                  <el-tag v-if="op.movedTo" type="primary" size="small">已移至上方直控</el-tag>
                </div>
                <div v-if="op.note" class="op-desc">{{ op.note }}</div>
                <div v-if="!op.available && op.reason" class="op-desc op-desc-warn">不可用原因：{{ op.reason }}</div>
                <!-- 不再把人支到生产页：本页现在就能填（2026-10-16 用户要求） -->
                <!-- movedTo 的条目右侧按钮是「去上方开关」，没有「填写参数」入口；
                     再显示「点填写参数…」等于指着不存在的按钮（2026-10-20 实测），故排除。 -->
                <div v-else-if="op.needsParams && !op.movedTo" class="op-desc">该操作需要参数，点「填写参数」按后端 Schema 逐项填写后提交。</div>
              </div>
              <div class="op-right">
                <span v-if="opState(op.id).phase !== 'idle'" class="op-state">
                  <i class="dot" :class="stateDot(opState(op.id).phase)"></i>{{ opState(op.id).text }}
                </span>
                <el-button v-if="op.movedTo" size="small" @click="scrollToMos">去上方开关</el-button>
                <el-button
                  v-else
                  size="small"
                  :type="op.risky ? 'warning' : 'primary'"
                  :plain="op.risky"
                  :loading="opBusy(op.id)"
                  :disabled="opBusy(op.id) || !op.available"
                  @click="onOpClick(op)"
                >
                  {{ !op.available ? '暂不可用' : op.needsParams ? '填写参数' : op.risky ? '确认后执行' : '执行' }}
                </el-button>
              </div>
              <el-alert v-if="opState(op.id).reason" type="error" :closable="false" class="op-reason" :title="opState(op.id).reason" />
            </div>
          </div>
        </div>
      </div>
    </div>

    <CollapseBlock
      block-id="history"
      title="操作历史"
      :summary="historyCollapseSummary"
      :collapsible="isMobileViewport"
      :lazy="true"
      :open="collapse.isOpen('history')"
      @toggle="collapse.toggle('history')"
    >
    <div class="card history-card">
      <div class="card-head">
        <span class="card-title">操作历史</span>
        <span class="card-hint">审计线索 · 最近 {{ historyRows.length }} 条</span>
      </div>
      <div class="history-list">
        <div v-for="h in historyRows" :key="h.id" class="history-item">
          <div class="h-top">
            <span class="h-type">{{ h.type }}</span>
            <el-tag :type="resultTag(h.result)" size="small" effect="plain">{{ resultText(h.result) }}</el-tag>
          </div>
          <div class="h-content">{{ h.content }}</div>
          <div class="h-meta">{{ h.time }}</div>
        </div>
        <div v-if="!historyRows.length" class="history-empty">暂无操作记录</div>
      </div>
    </div>
    </CollapseBlock>
  </section>
</template>

<script setup lang="ts">
import { ArrowRight, InfoFilled } from '@element-plus/icons-vue'
import CollapseBlock from '../CollapseBlock.vue'
import type { CollapseState } from '@/composables/useCollapseState'
import {
  resultTag,
  resultText,
  stateDot,
  type HistoryRow,
  type OpDef,
  type OpGroup,
  type OpState,
} from '../composables/bmsOperationModel'

/**
 * 受控操作（目录来自后端，按用途分组）+ 操作历史（审计线索）。
 *
 * 为什么这两块合成一个子组件：它们读的是**同一份受控操作数据**（目录 catalogs 与
 * 回执 histories），在同一行里并排；拆成两个子组件只会让「同一份回执驱动两处状态」
 * 变成两份 props 契约。
 *
 * 边界（状态仍全部在页面侧）：
 *   · 目录分组 / 执行状态机 / 参数表单 / 操作历史在 useBmsOperations；
 *     本组件只做展示与转发（onOpClick、loadOperations 以回调 props 下传，
 *     与拆分前页面里的同名 handler 一一对应，行为不变）。
 *   · 折叠记忆按设备存（collapse.isOpen/toggle）—— useCollapseState 由**页面**持有
 *     （键是 bms-collapse:<deviceId>，属于页级约定），这里只透传该对象。
 *   · 「去上方开关」滚动到页面里的 .mos-card（DOM 查询留在本组件，与拆分前一致）。
 */
defineProps<{
  totalOps: number
  opGroups: OpGroup[]
  openGroups: Record<string, boolean>
  toggleGroup: (id: string) => void
  riskCount: (g: OpGroup) => number
  opState: (id: string) => OpState
  opBusy: (id: string) => boolean
  onOpClick: (op: OpDef) => void
  catalogError: string
  catalogLoading: boolean
  loadOperations: () => void
  historyRows: HistoryRow[]
  historyCollapseSummary: string
  collapse: CollapseState
  isMobileViewport: boolean
}>()

/** movedTo 条目（set_mos_policy）的右侧按钮：滚回页面顶部的 MOS 直控卡，不在这里重复开关。 */
function scrollToMos() {
  const el = document.querySelector('.mos-card')
  if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
</script>

<style scoped>
.ops-history > .ops-card      { flex-basis: 600px; min-width: 400px; }
.ops-history > .history-card  { flex-basis: 320px; min-width: 300px; }
/* 需要撑满剩余高度的内容区（列表/网格）显式声明，未声明者按内容高度、自然留白 */
.row > .card > .op-groups,
.row > .card > .history-list { flex: 1 1 auto; min-height: 0; }

/* ══════════ 受控操作 ══════════ */
.state-legend { font-size: 12px; color: var(--el-text-color-secondary); }
.op-groups { display: flex; flex-direction: column; gap: 8px; }
.op-group { border: 1px solid var(--el-border-color-lighter); border-radius: var(--radius-sm); overflow: hidden; }
.op-group-head {
  display: flex; align-items: center; gap: 8px; width: 100%; min-height: 44px;
  padding: 8px 12px; border: none; background: var(--el-fill-color-lighter);
  cursor: pointer; text-align: left; font-size: 13px; color: var(--el-text-color-primary);
}
.op-group-head:hover { background: var(--el-fill-color-light); }
.op-caret { color: var(--el-text-color-secondary); transition: transform 0.2s; }
.op-caret.open { transform: rotate(90deg); }
.op-group-icon { color: var(--el-color-primary); }
/*
  中文标签必须禁逐字断行（2026-10-20 移动端实测）：
  390px 下分组头只有 290px，图标/caret/计数/风险标签/提示 挤占后，
  「读取信息」被逐字拆成 4 行（49×38px，像竖排），分组头高 54~73px 参差。
  用 word-break: keep-all 保证中文词不被拆开，再用 flex-wrap 让整块换行。
*/
.op-group-name { font-weight: 600; word-break: keep-all; white-space: nowrap; }
.op-group-count { font-size: 12px; color: var(--el-text-color-secondary); white-space: nowrap; flex-shrink: 0; }
.op-group-hint { font-size: 12px; color: var(--el-text-color-secondary); margin-left: auto; word-break: keep-all; }
.op-list { display: flex; flex-direction: column; }
.op-item { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border-top: 1px solid var(--el-border-color-lighter); flex-wrap: wrap; }
.op-info { flex: 1; min-width: 220px; }
.op-name-line { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.op-name { font-size: 13px; font-weight: 500; }
.op-desc { font-size: 12px; color: var(--el-text-color-secondary); margin-top: 3px; line-height: 1.6; }
.op-desc-warn { color: var(--el-color-warning); }
.op-right { display: flex; align-items: center; gap: 10px; flex-shrink: 0; }
.op-state { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--el-text-color-regular); }
.op-reason { flex-basis: 100%; margin: 4px 0 0; }
.dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; }
.dot-green { background: var(--el-color-success); }
.dot-red { background: var(--el-color-danger); }
.dot-orange { background: var(--el-color-warning); }
.dot-blue { background: var(--el-color-primary); }

/* ══════════ 操作历史 ══════════ */
.history-list { display: flex; flex-direction: column; gap: 8px; max-height: 520px; overflow-y: auto; }
.history-item { padding: 10px 12px; background: var(--el-fill-color-lighter); border-radius: var(--radius-sm); }
.h-top { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.h-type { font-size: 12px; font-weight: 600; }
.h-content { font-size: 12px; color: var(--el-text-color-regular); margin-top: 4px; line-height: 1.6; word-break: break-word; }
.h-meta { font-size: 11px; color: var(--el-text-color-secondary); margin-top: 4px; }
.history-empty { padding: 24px 12px; text-align: center; color: var(--el-text-color-secondary); font-size: 12px; }

@media (max-width: 1200px) {
  .ops-history > .ops-card { flex-basis: 420px; }
}

@media (max-width: 768px) {
  .history-card > .card-head > .card-title { display: none; }
  /* 操作历史：折叠头已是「操作历史 · 最近 N 条」，卡内「审计线索 · 最近 N 条」同屏重复 */
  .history-card > .card-head { display: none; }
  /* ── 受控操作：折叠分层后默认 1280 → ~370px ──
     ① 卡头那句「状态文案：已发送 → 执行中 → …」是状态机词表，窄屏要占 4 行 55px；
        每条操作执行后就地显示圆点+状态词，失败还有红色原因条，所以移动端只留标题+计数；
     ② 分组头：名称行 + 提示行（提示区分「只读」「会改运行状态」「需进厂模式」，
        是执行前的风险预判，不能删），内距压到 4px 让每条正好 44px 触摸目标；
     ③ 条目网格化：桌面是「信息块 220px + 右侧按钮」的 flex，窄屏会折成 3~4 行（实测 152px/条），
        改成「名称+标签 | 按钮」一行、说明独占整行。 */
  .ops-card .state-legend { display: none; }
  .op-groups { gap: 6px; }
  .op-group-head { flex-wrap: wrap; row-gap: 1px; align-items: center; gap: 6px; padding: 4px 10px; }
  .op-group-head { min-height: 44px; }
  .op-group-hint { margin-left: 0; flex-basis: 100%; font-size: 11px; line-height: 15px; }
  .op-item {
    display: grid;
    /* 动作列定宽 88px：用 auto 时按钮宽度随文案变（执行 44 / 填写参数 84 / 确认后执行 99），
       每行按钮起点都不一样，视觉上像「浮在右边」；定宽后 5 行按钮左边缘对齐。 */
    grid-template-columns: minmax(0, 1fr) 88px;
    column-gap: 8px;
    row-gap: 2px;
    align-items: center;
    padding: 8px 10px;
  }
  .op-info { display: contents; }
  .op-name-line { grid-column: 1; grid-row: 1; }
  .op-desc { grid-column: 1 / -1; margin-top: 0; font-size: 11px; line-height: 16px; }
  .op-right { grid-column: 2; grid-row: 1; gap: 6px; justify-content: flex-end; flex-wrap: wrap; }
  /* 判别标签（风险/普通/需参数/暂不可用）降一档：默认尺寸在 393px 下会把名称行挤成 3 行 */
  /* 同 BmsStatusCards 的说明：与页面 .page-body :deep(.el-tag) 同特异性，拆开后顺序不可依赖，
     多带一级 .ops-card 保证「内距 4px」与拆分前一致。 */
  .ops-card .op-name-line :deep(.el-tag) { font-size: 10px; height: 18px; line-height: 16px; padding: 0 4px; }
  .op-right :deep(.el-button) { padding: 0 8px; font-size: 12px; }
  .op-reason { grid-column: 1 / -1; margin: 2px 0 0; }

  /* 硬 min-width 在窄屏必须清零（见公共基座同名说明） */
  .ops-history > .ops-card,
  .ops-history > .history-card {
    flex: 1 1 100%;
    min-width: 0;
  }
}
@media (pointer: coarse) {
  .op-group-head { min-height: 44px; }
}
</style>

<style scoped src="./bmsDetail.shared.css"></style>
