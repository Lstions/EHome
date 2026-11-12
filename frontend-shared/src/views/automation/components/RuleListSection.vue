<template>
  <section class="card rules-card">
    <div class="card-head">
      <span class="card-title">规则<el-tag v-if="rules.length" size="small" class="count-tag">{{ rules.length }}</el-tag></span>
      <el-button type="primary" :icon="Plus" data-test="create-rule" @click="onCreate">创建规则</el-button>
    </div>
    <!--
      2026-10-21 第二轮截图精读（P1 信息损失）：393 下这张 7 列表格自然宽 970px，
      视口内只剩「名称 + 触发器」两列，且名称被 show-overflow-tooltip 截成
      「客厅高温自动开…」—— 触屏没有 hover，tooltip 不可达，横滑也只是多看一列。
      修法：窄屏（≤768px）改**卡片列表**（名称全显示 + 触发器类型徽标 + 触发条件/动作摘要
      + 冷却/熔断 + 启用开关 + 操作按钮，顶部虚线之外无信息损失）；桌面（>768px）表格
      与固定列逐像素不变。
    -->
    <!-- 桌面：宽表（结构 / 固定列 / 横滑提示与改前一致） -->
    <div v-if="!isMobile" class="mobile-table-wrapper">
      <div class="mobile-table-hint">← 左右滑动查看完整表格 →</div>
    <el-table :data="rules" v-loading="loading" data-test="rules-table">
      <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
      <el-table-column label="触发器" min-width="180">
        <template #default="{ row }">
          <div class="trigger-cell">
            <el-tag size="small" class="trigger-type-tag">{{ triggerTypeText(row.trigger_type) }}</el-tag>
            <span class="mono trigger-summary">{{ triggerSummary(asRule(row)) }}</span>
          </div>
        </template>
      </el-table-column>
      <!-- R2: 动作列由"单动作摘要"改为"多动作摘要 + 风险计数"。风险计数让用户在
           列表页就能发现风险规则，不必逐条打开编辑器。计数读的是后端下发的
           requires_confirmation（唯一判定点），前端不自己算风险等级。 -->
      <el-table-column label="动作" min-width="180">
        <template #default="{ row }">
          <div class="action-cell">
            <el-tag size="small" :type="row.action_type === 'device_action' ? 'warning' : 'info'">
              {{ row.action_type === 'device_action' ? '设备动作' : '通知' }} ×{{ ruleActions(asRule(row)).length }}
            </el-tag>
            <el-tag v-if="ruleRiskCount(asRule(row))" type="danger" size="small" data-test="rule-risk-count">
              <span aria-hidden="true">⚠</span> 含 {{ ruleRiskCount(asRule(row)) }} 个风险动作
            </el-tag>
            <span class="mono action-summary">{{ actionSummary(asRule(row)) }}</span>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="冷却/熔断" width="120">
        <template #default="{ row }">
          <span class="mono">{{ row.cooldown_sec }}s / {{ row.max_daily_exec === 0 ? '不限' : row.max_daily_exec }}</span>
        </template>
      </el-table-column>
      <el-table-column label="确认制" width="90">
        <template #default="{ row }">
          <el-tag v-if="row.require_confirmed" type="warning" size="small">需确认</el-tag>
          <span v-else class="text-muted">-</span>
        </template>
      </el-table-column>
      <el-table-column label="启用" width="80">
        <template #default="{ row }">
          <!-- 可访问名带**规则名**：表格里每行都有一个一模一样的开关，
               没有名字时屏幕阅读器只读「开关」，用户无法分辨改的是哪条规则。 -->
          <el-switch
            :model-value="row.enabled"
            :aria-label="`${asRule(row).name} 启用`"
            data-test="rule-enabled"
            @change="(v: string | number | boolean) => onToggle(asRule(row), v === true)"
          />
        </template>
      </el-table-column>
      <!-- F26 裁决 D2：该列在窄屏**取消固定**（原 width="180" fixed="right"）。
           实测（390px，48 点 elementFromPoint 网格）：固定列 180px 压在「启用」列上，
           行内 el-switch 命中自身 0/48、命中固定列 48/48 —— 开关 100% 不可点；
           桌面 1440px 命中 48/48（完全正常）。根因是粘性列的绘制顺序恒在普通列之上
           （见 theme.css「固定列」小节），与列宽无关，故不适用收窄列宽那条路：
           若按 EdgeDeviceList 范式改成图标按钮，桌面操作列会从 180px 降到 96px，
           直接改变本页桌面视觉密度（任务书禁止）。
           改法：宽度与内容不变，只在窄屏（<768px，与 useResponsive 的 BREAKPOINTS.md
           同源）把 fixed 置 false。EP 的 table-column 对 fixed 注册了 watch 并触发
           scheduleLayout（element-plus/es/components/table/src/table-column/watcher-helper.mjs），
           运行期翻转会重算固定列集合；组件挂载时 isMobile 已是正确值，
           桌面渲染路径与改前逐字节等价。 -->
      <el-table-column label="操作" width="180" :fixed="isMobile ? false : 'right'">
        <template #default="{ row }">
          <el-button link type="warning" size="small" :loading="triggeringId === row.id" data-test="trigger-rule" @click="onTrigger(asRule(row))">触发</el-button>
          <el-button link type="primary" size="small" @click="onEdit(asRule(row))">编辑</el-button>
          <el-button link type="danger" size="small" @click="onDelete(asRule(row))">删除</el-button>
        </template>
      </el-table-column>
      <template #empty>暂无规则，点击右上角创建</template>
    </el-table>
    </div>
    <!-- 窄屏：规则卡片列表（每张卡承载桌面 7 列的全部信息，无省略号截断） -->
    <div v-else class="rule-cards" data-test="rules-cards">
      <p v-if="loading" class="cards-empty">正在加载规则…</p>
      <template v-else>
      <article v-for="row in rules" :key="row.id" class="rule-card" data-test="rule-card">
        <div class="rule-card__head">
          <h3 class="rule-card__name">{{ asRule(row).name }}</h3>
          <el-switch
            :model-value="row.enabled"
            :aria-label="`${asRule(row).name} 启用`"
            data-test="rule-enabled"
            @change="(v: string | number | boolean) => onToggle(asRule(row), v === true)"
          />
        </div>
        <div class="rule-card__badges">
          <el-tag size="small" class="trigger-type-tag">{{ triggerTypeText(row.trigger_type) }}</el-tag>
          <el-tag size="small" :type="row.action_type === 'device_action' ? 'warning' : 'info'">
            {{ row.action_type === 'device_action' ? '设备动作' : '通知' }} ×{{ ruleActions(asRule(row)).length }}
          </el-tag>
          <el-tag v-if="ruleRiskCount(asRule(row))" type="danger" size="small" data-test="rule-risk-count">
            <span aria-hidden="true">⚠</span> 含 {{ ruleRiskCount(asRule(row)) }} 个风险动作
          </el-tag>
          <el-tag v-if="row.require_confirmed" type="warning" size="small">需确认</el-tag>
        </div>
        <dl class="rule-card__meta">
          <div>
            <dt>触发条件</dt>
            <dd class="mono">{{ triggerSummary(asRule(row)) }}</dd>
          </div>
          <div>
            <dt>动作</dt>
            <dd class="mono">{{ actionSummary(asRule(row)) }}</dd>
          </div>
          <div>
            <dt>冷却/熔断</dt>
            <dd class="mono">{{ row.cooldown_sec }}s / {{ row.max_daily_exec === 0 ? '不限' : row.max_daily_exec }}</dd>
          </div>
        </dl>
        <div class="rule-card__ops">
          <el-button type="warning" size="small" plain :loading="triggeringId === row.id" data-test="trigger-rule" @click="onTrigger(asRule(row))">触发</el-button>
          <el-button type="primary" size="small" plain @click="onEdit(asRule(row))">编辑</el-button>
          <el-button type="danger" size="small" plain @click="onDelete(asRule(row))">删除</el-button>
        </div>
      </article>
      <p v-if="rules.length === 0" class="cards-empty">暂无规则，点击右上角创建</p>
      </template>
    </div>
  </section>
</template>

<script setup lang="ts">
import { Plus } from '@element-plus/icons-vue'
import type { AutomationRule } from '@/api/automation'
import { actionSummary, ruleActions, ruleRiskCount, triggerSummary, triggerTypeText } from '../automationFormat'

/**
 * 规则列表区（桌面 7 列宽表 / 窄屏卡片列表）。
 *
 * 职责：只渲染「自动化策略」这张卡片 —— 卡片头（标题 + 计数徽标 + 创建按钮）、
 * 桌面宽表、窄屏卡片列表，以及两种形态下的加载态/空态。不加载数据、不调接口、
 * 不弹确认框。
 *
 * props:
 *   rules        规则行（页面 fetchRules 的结果；子组件只读，不复制、不缓存）
 *   loading      rules 的加载态（表格 v-loading / 卡片「正在加载规则…」）
 *   isMobile     <768px 渲染卡片、>768px 渲染表格（与 useResponsive 同断点）
 *   triggeringId 正在手动触发的规则 id（只用于该行「触发」按钮的 loading）
 *
 * emits:
 *   create                  点「创建规则」
 *   edit(rule)              点行内「编辑」
 *   delete(rule)            点行内「删除」（危险确认仍由页面弹，子组件不碰 feedback）
 *   toggle(rule, enabled)   启用开关
 *   trigger(rule)           点行内「触发」
 *
 * 为什么这样切：
 *   · 数据与动作留在页面是硬约束：fetchRules / onToggle / onDelete / onTrigger 都涉及
 *     请求体、错误提示与页面级刷新（规则变更后还要刷新事件表里规则名的回链），
 *     子组件拿不到这些上下文，硬塞进来只会把页面逻辑复制一份；
 *   · 表格与卡片是同一份数据的两种布局，必须一起搬 —— 只搬一个的话，窄屏与桌面会
 *     各自维护一套「触发器摘要 / 风险计数」的渲染，必然漂移（这正是本仓反复出现的
 *     两套录入/两套措辞问题的同型缺陷）；
 *   · 行内控件全部走 emit：危险确认（feedback.confirmDanger）与「触发中」状态因此
 *     仍只有页面一个事实源，既有测试「点删除 → 弹确认 → 调 deleteRule」的接线不变。
 */
defineProps<{
  rules: AutomationRule[]
  loading: boolean
  isMobile: boolean
  triggeringId: number | null
}>()

const emit = defineEmits<{
  (e: 'create'): void
  (e: 'edit', rule: AutomationRule): void
  (e: 'delete', rule: AutomationRule): void
  (e: 'toggle', rule: AutomationRule, enabled: boolean): void
  (e: 'trigger', rule: AutomationRule): void
}>()

/** el-table 作用域槽的 row 在 EP 类型里是内部 DefaultRow（未从包根导出），此处做一次命名类型的边界收窄（非 any）。 */
const asRule = (row: unknown) => row as AutomationRule

// 行内动作 → 语义化事件（命名沿用页面里的 on* 前缀，便于对照搬走前的实现）。
function onCreate() {
  emit('create')
}
function onEdit(rule: AutomationRule) {
  emit('edit', rule)
}
function onDelete(rule: AutomationRule) {
  emit('delete', rule)
}
function onToggle(rule: AutomationRule, enabled: boolean) {
  emit('toggle', rule, enabled)
}
function onTrigger(rule: AutomationRule) {
  emit('trigger', rule)
}
</script>

<style scoped>
/* 规则卡片区样式：原先写在 AutomationRules.vue 的 scoped 样式里。
   scoped 样式不作用于子组件内部 DOM，片段搬走时规则必须一起搬，否则搬完就掉样式。
   .card / .card-head / .card-title / .count-tag / .mono / .text-muted 与事件区共用，
   scoped 无法跨组件共享，两边各留一份（重复 3~5 行好过让样式失效）。 */
.card { background: var(--el-bg-color); border-radius: 8px; padding: 16px; }
/* 卡片头：允许换行、标题单行不压缩（规则区只有"标题 + 创建按钮"，不受 332px 筛选条挤压，
   但两区共用同一套卡片语言，故保持与事件区一致的声明）。 */
.card-head { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; row-gap: 8px; margin-bottom: 12px; }
.card-title { font-size: 15px; font-weight: 600; white-space: nowrap; flex: 0 0 auto; }
.count-tag { margin-left: 8px; }
.mono { font-family: monospace; }
.text-muted { color: var(--el-text-color-secondary); }
.trigger-cell, .action-cell { display: flex; align-items: center; gap: 6px; }
.trigger-type-tag { flex-shrink: 0; }
.trigger-summary, .action-summary { font-size: 12px; }

/* ============================================================================
   窄屏卡片列表（2026-10-21 第二轮：393 宽表只剩 2 列 + 名称被省略号截断）
   ========================================================================== */
@media (max-width: 768px) {
  /* 卡片长度不受列宽限制：名称/详情一律换行全显，不用 ellipsis 隐藏数据。
     视觉语言沿用 Dashboard .latest-card / DataPanel .data-card（浅描边 + 8px 圆角 + 双列 meta）。 */
  .rule-cards { display: flex; flex-direction: column; gap: 10px; }
  .rule-card {
    border: 1px solid var(--el-border-color-lighter);
    border-radius: var(--radius-md, 8px);
    padding: 10px 12px;
    background: var(--el-bg-color);
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }
  .rule-card__head { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; }
  /* 卡片标题：完整名称换行显示（被修掉的正是「客厅高温自动开…」这类省略号截断） */
  .rule-card__name {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
    line-height: 1.4;
    color: var(--el-text-color-primary);
    overflow-wrap: anywhere;
    min-width: 0;
  }
  .rule-card__head :deep(.el-switch) { flex: 0 0 auto; min-height: 44px; }
  .rule-card__badges { display: flex; flex-wrap: wrap; gap: 6px; }
  .rule-card__meta { display: flex; flex-direction: column; gap: 4px; margin: 0; min-width: 0; }
  .rule-card__meta > div { display: flex; gap: 8px; font-size: 13px; line-height: 1.5; min-width: 0; }
  .rule-card__meta dt { flex: 0 0 auto; color: var(--el-text-color-secondary); }
  .rule-card__meta dd { margin: 0; min-width: 0; color: var(--el-text-color-regular); overflow-wrap: anywhere; }
  .rule-card__ops { display: flex; flex-wrap: wrap; gap: 8px; }
  .rule-card__ops :deep(.el-button) { margin-left: 0; min-height: 44px; padding-left: 14px; padding-right: 14px; }
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
}
</style>
