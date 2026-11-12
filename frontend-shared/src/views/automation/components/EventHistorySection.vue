<template>
  <section class="card events-card">
    <div class="card-head">
      <span class="card-title">触发历史<el-tag v-if="events.length" size="small" class="count-tag">{{ events.length }}</el-tag></span>
      <div class="event-filters">
        <el-select :model-value="filterRuleId" placeholder="按规则过滤" clearable size="small" style="width: 140px" data-test="filter-rule" @update:model-value="onFilterRuleChange" @change="onFilterChange">
          <el-option v-for="r in rules" :key="r.id" :label="r.name" :value="r.id" />
        </el-select>
        <el-select :model-value="filterResult" placeholder="按结果过滤" clearable size="small" style="width: 140px" data-test="filter-result" @update:model-value="onFilterResultChange" @change="onFilterChange">
          <el-option v-for="opt in resultOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
        </el-select>
        <el-button link size="small" data-test="refresh-events" @click="onRefresh">刷新</el-button>
      </div>
    </div>
    <!--
      2026-10-21 第二轮截图精读（P1 遮挡）：393 下 910px 自然宽的 8 列表格里，右侧
      fixed="right" 的「操作」列（100px，占视口 31%）压住「规则」列文字
      （实测 td 192~312 被固定列覆盖 59px、「结果」覆盖 41px）；上一轮无数据时把它判为
      待确认 P2，本轮用注入数据复测确认确有遮挡 + 横滑信息损失。
      修法：窄屏（≤768px）改**卡片列表**（时间 + 规则名 + 结果 + 触发值/来源/命令 ID/详情 +
      待确认操作），不再渲染表格、不再存在固定列；桌面（>768px）表格与 fixed 逐像素不变。
    -->
    <!-- 桌面：宽表（结构 / fixed 操作列 / 横滑提示与改前一致） -->
    <div v-if="!isMobile" class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
    <el-table :data="events" v-loading="loading" data-test="events-table">
      <el-table-column label="时间" min-width="160">
        <template #default="{ row }">{{ formatTime(row.triggered_at) }}</template>
      </el-table-column>
      <el-table-column label="规则" width="120">
        <template #default="{ row }">
          <span class="mono" data-test="event-rule-name">{{ ruleName(row.rule_id) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="结果" width="110">
        <template #default="{ row }">
          <el-tag :type="resultTagType(row.result)" size="small">{{ resultText(row.result) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="触发值" width="100">
        <template #default="{ row }">
          <span v-if="row.trigger_value !== undefined" class="mono">{{ formatValue(row.trigger_value) }}</span>
          <span v-else class="text-muted">-</span>
        </template>
      </el-table-column>
      <el-table-column label="来源" width="70">
        <template #default="{ row }">
          <el-tag v-if="row.trigger_source === 'manual'" type="warning" size="small">手动</el-tag>
          <span v-else class="text-muted">自动</span>
        </template>
      </el-table-column>
      <el-table-column label="命令 ID" width="110">
        <template #default="{ row }">
          <el-button v-if="row.command_id" link type="primary" size="small" @click="onGoCommand(row.command_id)">
            {{ row.command_id }}
          </el-button>
          <span v-else class="text-muted">-</span>
        </template>
      </el-table-column>
      <el-table-column prop="detail" label="详情" min-width="140" show-overflow-tooltip />
      <!-- 2026-10-21 审计复核结论：本列 width=100 < 窄屏表格盒（280~310px），
           F26 契约与实测探针都判定**不遮挡**（48/48 命中自身），故保持 fixed="right"。
           C-ops 审计曾把它标为「待确认 P2」（当时无数据、只看到白色条带），
           本轮按 F26 既有裁决回退 —— 不用未验证的猜测改动已通过的契约。 -->
      <el-table-column label="操作" width="100" fixed="right">
        <template #default="{ row }">
          <el-button
            v-if="row.result === 'pending_confirm'"
            link
            type="warning"
            size="small"
            data-test="confirm-event"
            @click="onConfirmEvent(asEvent(row))"
          >
            确认执行
          </el-button>
        </template>
      </el-table-column>
      <!-- F3：区分"没配策略"与"策略没触发/被抑制" —— 后者常见成因是 cooldown 或日熔断，
           只说「暂无触发事件」会让用户以为页面坏了（审计 R2-C2 口径）。 -->
      <template #empty>
        <div class="table-empty">
          <p>{{ rules.length === 0 ? '尚未创建自动化策略' : '暂无触发事件' }}</p>
          <p class="table-empty-hint">
            {{ rules.length === 0
              ? '在上方「自动化策略」中创建规则后，命中的事件会出现在这里。'
              : '策略已就绪但尚未产生事件：确认条件可达，并留意冷却期（cooldown）与日熔断会抑制触发。' }}
          </p>
        </div>
      </template>
    </el-table>
    </div>
    <!-- 窄屏：触发事件卡片列表（时间 / 规则名 / 结果 / 触发值 / 来源 / 命令 ID / 详情 / 待确认操作） -->
    <div v-else class="event-cards" data-test="events-cards">
      <p v-if="loading" class="cards-empty">正在加载触发事件…</p>
      <template v-else>
        <article v-for="row in events" :key="row.id" class="event-card" data-test="event-card">
          <div class="event-card__head">
            <el-tag :type="resultTagType(row.result)" size="small">{{ resultText(row.result) }}</el-tag>
            <span class="mono event-card__time">{{ formatTime(row.triggered_at) }}</span>
            <el-tag v-if="row.trigger_source === 'manual'" type="warning" size="small">手动</el-tag>
            <span v-else class="event-card__source">自动</span>
          </div>
          <p class="event-card__rule" data-test="event-rule-name">{{ ruleName(row.rule_id) }}</p>
          <dl class="event-card__meta">
            <div>
              <dt>触发值</dt>
              <dd class="mono">{{ row.trigger_value !== undefined ? formatValue(row.trigger_value) : '-' }}</dd>
            </div>
            <div v-if="row.command_id">
              <dt>命令 ID</dt>
              <dd class="mono event-card__cmd">
                <span class="event-card__cmd-id">{{ row.command_id }}</span>
                <el-button link type="primary" size="small" @click="onGoCommand(row.command_id)">查看</el-button>
              </dd>
            </div>
            <div v-if="row.detail">
              <dt>详情</dt>
              <dd>{{ row.detail }}</dd>
            </div>
          </dl>
          <div v-if="row.result === 'pending_confirm'" class="event-card__ops">
            <el-button type="warning" size="small" plain data-test="confirm-event" @click="onConfirmEvent(asEvent(row))">确认执行</el-button>
          </div>
        </article>
        <!-- F3：卡片列表沿用与表格 #empty 完全相同的"没配策略 / 没触发"区分口径 -->
        <div v-if="events.length === 0" class="cards-empty">
          <p>{{ rules.length === 0 ? '尚未创建自动化策略' : '暂无触发事件' }}</p>
          <p class="cards-empty__hint">
            {{ rules.length === 0
              ? '在上方「自动化策略」中创建规则后，命中的事件会出现在这里。'
              : '策略已就绪但尚未产生事件：确认条件可达，并留意冷却期（cooldown）与日熔断会抑制触发。' }}
          </p>
        </div>
      </template>
    </div>
    <slot name="pagination" />
  </section>
</template>

<script setup lang="ts">
import type { AutomationEvent, AutomationEventResult, AutomationRule } from '@/api/automation'
import { formatTime, formatValue, resultOptions, resultTagType, resultText } from '../automationFormat'

/**
 * 触发历史区（筛选条 / 桌面 8 列宽表 / 窄屏卡片列表 / 空态 / 分页位置）。
 *
 * 职责：只渲染「触发历史」这张卡片。不加载数据、不调接口、不弹确认框；
 * 筛选值、页码、每页条数仍由页面持有（fetchEvents 是页面级动作）。
 *
 * props:
 *   events        当前页事件（页面 fetchEvents 的结果）
 *   loading       events 的加载态
 *   isMobile      <768px 渲染卡片、>768px 渲染表格（与 useResponsive 同断点）
 *   rules         规则行 —— 唯一用途是把事件里的 rule_id 回链成规则名
 *                 （规则已删除时回退 #id，不留空白）
 *   filterRuleId  「按规则过滤」当前值（受控：子组件只回抛，不自己存）
 *   filterResult  「按结果过滤」当前值（受控）
 *
 * emits:
 *   refresh                       点「刷新」
 *   update:filterRuleId(value)    规则筛选值变化
 *   update:filterResult(value)    结果筛选值变化
 *   filter-change                 任一筛选值变化后（页面据此 fetchEvents；与既有
 *                                 v-model + @change="fetchEvents" 的时序一致：
 *                                 先更新值再拉数据）
 *   confirm-event(event)          待确认事件上点「确认执行」（危险确认由页面弹）
 *   go-command(commandId)         点「命令 ID」回链查看执行记录（查询与弹窗在页面）
 *
 * 为什么这样切：
 *   · 分页控件**不出现在本文件的模板里**，而是经 #pagination 插槽由页面注入：
 *     listPageConventions 门禁按「路由页模板里有 el-pagination」判定列表页，
 *     把控件搬进子组件会让 AutomationRules.vue 退出该门禁的分母 —— 门禁失去对象。
 *     插槽位置与搬走前完全一致，最终 DOM 不变；对应样式（.events-pagination）也留在页面；
 *   · 事件数据的取数（页码/筛选重置、rule_id/result 请求体、错误提示、WS 推送刷新）
 *     全部是页面级语义，子组件只处理「显示 + 抛事件」；
 *   · 表格与卡片必须一起搬：结果文案/配色（resultText / resultTagType）与触发值格式化
 *     在两处都必须同源，分开维护必然漂移；
 *   · 筛选用「受控 props + update 事件」而不是在子组件里存一份：页面的 fetchEvents
 *     依赖筛选值做页码重置（§3.2.6），存两份就会出现「子组件显示 A、请求发 B」。
 */
const props = defineProps<{
  events: AutomationEvent[]
  loading: boolean
  isMobile: boolean
  rules: AutomationRule[]
  filterRuleId: number | undefined
  filterResult: AutomationEventResult | undefined
}>()

const emit = defineEmits<{
  (e: 'refresh'): void
  (e: 'update:filterRuleId', value: number | undefined): void
  (e: 'update:filterResult', value: AutomationEventResult | undefined): void
  (e: 'filter-change'): void
  (e: 'confirm-event', event: AutomationEvent): void
  (e: 'go-command', commandId: string): void
}>()

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asEvent = (row: unknown) => row as AutomationEvent

/** 事件只持久化 rule_id：回链规则名展示，规则已被删除时回退 #id（不留空白）。 */
function ruleName(ruleId: number): string {
  const hit = props.rules.find(r => r.id === ruleId)
  return hit ? hit.name : '#' + ruleId
}

// 筛选：先回抛新值（页面 ref 同步更新），再通知页面拉数据 —— 与搬走前
// v-model + @change 的触发顺序一致，fetchEvents 因此能读到新值。
function onFilterRuleChange(value: unknown) {
  emit('update:filterRuleId', value as number | undefined)
}
function onFilterResultChange(value: unknown) {
  emit('update:filterResult', value as AutomationEventResult | undefined)
}
function onFilterChange() {
  emit('filter-change')
}

function onRefresh() {
  emit('refresh')
}
function onConfirmEvent(event: AutomationEvent) {
  emit('confirm-event', event)
}
function onGoCommand(commandId: string) {
  emit('go-command', commandId)
}
</script>

<style scoped>
/* 触发历史区样式：原先写在 AutomationRules.vue 的 scoped 样式里，随片段一起搬。
   .card / .card-head / .card-title / .count-tag / .mono / .text-muted 与规则区共用，
   scoped 无法跨组件共享，两边各留一份。 */
.card { background: var(--el-bg-color); border-radius: 8px; padding: 16px; }
/* 2026-10-21 审计 C-ops P1：「触发历史」卡片头改前不可换行 —— 标题被右侧 332px 的
   筛选条挤到 15×84（逐字竖排），筛选条右界还溢出卡片 14px（卡片右 365 / 筛选右 379）。
   修复：卡片头允许换行（筛选条整体落到第二行）、标题单行不压缩、筛选条自身也换行。 */
.card-head { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; row-gap: 8px; margin-bottom: 12px; }
.card-title { font-size: 15px; font-weight: 600; white-space: nowrap; flex: 0 0 auto; }
.count-tag { margin-left: 8px; }
.mono { font-family: monospace; }
.text-muted { color: var(--el-text-color-secondary); }
.event-filters { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; justify-content: flex-end; min-width: 0; }
/* F3：表格内的空态（说明原因 + 下一步），与全页 EmptyState 保持同一种信息密度。 */
.table-empty { color: var(--el-text-color-secondary); }
.table-empty p { margin: 0; }
.table-empty-hint { margin-top: 4px; font-size: 12px; color: var(--el-text-color-placeholder); }

/* ============================================================================
   窄屏卡片列表（2026-10-21 第二轮：393 下 8 列表格右侧固定列压住「规则」列文字）
   ========================================================================== */
@media (max-width: 768px) {
  .event-cards { display: flex; flex-direction: column; gap: 10px; }
  .event-card {
    border: 1px solid var(--el-border-color-lighter);
    border-radius: var(--radius-md, 8px);
    padding: 10px 12px;
    background: var(--el-bg-color);
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }
  .event-card__head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
  .event-card__time { font-size: 13px; color: var(--el-text-color-regular); }
  .event-card__source { font-size: 12px; color: var(--el-text-color-secondary); }
  .event-card__rule {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
    line-height: 1.4;
    color: var(--el-text-color-primary);
    overflow-wrap: anywhere;
  }
  .event-card__meta { display: flex; flex-direction: column; gap: 4px; margin: 0; min-width: 0; }
  .event-card__meta > div { display: flex; gap: 8px; font-size: 13px; line-height: 1.5; min-width: 0; }
  .event-card__meta dt { flex: 0 0 auto; color: var(--el-text-color-secondary); }
  .event-card__meta dd { margin: 0; min-width: 0; color: var(--el-text-color-regular); overflow-wrap: anywhere; }
  .event-card__ops { display: flex; flex-wrap: wrap; gap: 8px; }
  .event-card__ops :deep(.el-button) { margin-left: 0; min-height: 44px; padding-left: 14px; padding-right: 14px; }
  .event-card__meta dd:last-child, .event-card__cmd { overflow-wrap: anywhere; }
  .event-card__cmd { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .event-card__cmd-id { word-break: break-all; }
  .event-card__cmd :deep(.el-button) { margin-left: 0; min-height: 44px; }
  /* 空态/加载态必须可见且有解释（卡片列表下不渲染表格 #empty 插槽） */
  .cards-empty {
    margin: 0;
    padding: 16px 8px;
    text-align: center;
    font-size: 13px;
    line-height: 1.6;
    color: var(--el-text-color-secondary);
  }
  .cards-empty p { margin: 0; }
  .cards-empty__hint { margin-top: 4px; font-size: 12px; color: var(--el-text-color-placeholder); }
}
</style>
