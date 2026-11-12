<template>
  <el-form-item label="动作" required>
    <div class="action-toolbar">
      <span class="text-muted" data-test="action-count">
        共 {{ actions.length }} 个动作{{ riskyCount
          ? `，其中 ${riskyCount} 个风险动作（保存前需逐项确认）`
          : '，无风险动作（保存时不会弹出二次确认）' }}
      </span>
      <el-button type="primary" size="small" data-test="add-action" @click="emit('add')">+ 添加动作</el-button>
    </div>
    <ul class="action-list" data-test="action-list">
      <!-- R7: 动作区由「单动作字段组」升级为**多动作卡片列表**。
           为什么用纵向卡片而不是表格（设计 D-3）：① 卡片天然容纳长度不定的
           「风险原因」文本；② 上移/下移是**顺序语义**操作，纵向列表的视觉顺序
           与执行顺序一致；③ 复用既有 DeviceControlPanel 的卡片语言
           （圆角 8px + --el-border-color-lighter 边框），不引入新视觉。 -->
      <li
        v-for="(action, index) in actions"
        :key="action.uid"
        class="action-card"
        :class="{ 'is-risk': !!helpers.badge(action) }"
        :data-risk="helpers.badge(action) ? '1' : '0'"
        data-test="action-card"
      >
        <div class="action-card__head">
          <span class="action-card__index" aria-hidden="true">{{ index + 1 }}</span>
          <span class="action-card__name">{{ helpers.title(action) }}</span>
          <!-- 风险徽标：**图标 + 文字**，不只用颜色（设计 D-6 四重编码）。
               红绿色盲用户看不到红色徽标，文字与图标是唯一能读出"风险"的路径。
               第三种状态（目录未加载 / 动作不在目录里）**不渲染任何等级徽标** ——
               把未知显示成「普通」等于替后端断言"它不危险"，见 riskPresentation 注释。 -->
          <el-tag v-if="helpers.badge(action)" type="danger" size="small" class="risk-badge" data-test="risk-badge">
            <span aria-hidden="true">{{ helpers.badge(action)!.icon }}</span> {{ helpers.badge(action)!.label }}
          </el-tag>
          <el-tag v-else-if="helpers.riskState(action) === 'normal'" type="info" size="small" class="normal-badge" data-test="normal-badge">
            <span aria-hidden="true">{{ NORMAL_BADGE.icon }}</span> {{ NORMAL_BADGE.label }}
          </el-tag>
          <el-tag v-else type="info" size="small" effect="plain" class="unknown-badge" data-test="unknown-badge">
            等级未知
          </el-tag>
          <span class="action-card__ops">
            <el-button
              link size="small" :disabled="index === 0" data-test="move-up"
              :aria-label="`上移第 ${index + 1} 个动作`" @click="emit('move', action.uid, -1)"
            >↑ 上移</el-button>
            <el-button
              link size="small" :disabled="index === actions.length - 1" data-test="move-down"
              :aria-label="`下移第 ${index + 1} 个动作`" @click="emit('move', action.uid, 1)"
            >↓ 下移</el-button>
            <el-button
              link type="primary" size="small" data-test="edit-action-params"
              :aria-label="`编辑第 ${index + 1} 个动作的参数`" @click="emit('open-params', action.uid)"
            >编辑参数</el-button>
            <el-button
              link type="danger" size="small" data-test="remove-action"
              :aria-label="`删除第 ${index + 1} 个动作`" @click="emit('remove', action.uid)"
            >删除</el-button>
          </span>
        </div>
        <dl class="action-meta">
          <div>
            <dt>动作类型</dt>
            <dd>
              <el-select v-model="action.action_type" size="small" :aria-label="`第 ${index + 1} 个动作的类型`" :data-test="`action-type-${index}`" @change="emit('type-change', action)">
                <el-option label="设备动作" value="device_action" />
                <el-option label="通知" value="notification" />
              </el-select>
            </dd>
          </div>
          <div v-if="action.action_type === 'device_action'">
            <dt>目标设备</dt>
            <dd>
              <!-- 传整张卡片（而不只是 id）：切换设备后要判定"已选动作在新目录里还在不在"，
                   并可能清空它（见 onCardDeviceChange） -->
              <el-select v-model="action.action_device_id" filterable size="small" :aria-label="`第 ${index + 1} 个动作的目标设备`" :data-test="`action-device-${index}`" @change="emit('device-change', action)">
                <el-option v-for="d in devices" :key="d.id" :label="d.name" :value="d.id" />
              </el-select>
            </dd>
          </div>
          <div v-else>
            <dt>通知级别</dt>
            <dd>
              <el-select v-model="action.action_level" size="small" :aria-label="`第 ${index + 1} 个动作的通知级别`" :data-test="`action-level-${index}`">
                <el-option label="信息" value="info" />
                <el-option label="警告" value="warning" />
                <el-option label="严重" value="critical" />
              </el-select>
            </dd>
          </div>
          <div v-if="action.action_type === 'device_action'">
            <dt>动作 ID</dt>
            <dd>
              <!-- 动作 ID：从**裸输入框**升级为「目录下拉 + 允许手输」。
                   为什么这是缺陷而不是特色：动作目录（actionCatalog）早就为了渲染风险徽标
                   加载在内存里了，却让用户凭记忆背 action_id —— 填一个目录里不存在的动作
                   要等到保存时被后端 400 才知道，而界面从头到尾没告诉过他有哪些可选。
                   为什么仍保留 allow-create 手输：目录可能取不到（设备离线 / 驱动未声明）
                   或设备换型，此时阻断建规则比让用户先填一个"待后端校验"的 ID 更糟
                   （与既有降级口径一致：目录拿不到不阻断建规则）。 -->
              <el-select
                v-model="action.action_id"
                filterable
                allow-create
                default-first-option
                size="small"
                :placeholder="helpers.idPlaceholder(action)"
                :aria-label="`第 ${index + 1} 个动作的 ID`"
                :data-test="`action-id-select-${index}`"
                @change="emit('action-id-change', action)"
              >
                <el-option
                  v-for="option in helpers.options(action)"
                  :key="option.value"
                  :label="option.label"
                  :value="option.value"
                />
              </el-select>
              <!-- 目录提示：三种互斥状态各自说清，绝不把"没取到"说成"没有"。
                   ① 空态（取回失败或确实没有动作）：仍可手输，明说可能是设备离线/驱动未声明；
                   ② 全部不可用：目录**有**动作但一台都下发不了 —— 与①分开，否则用户会去查驱动；
                   ③ 不在目录中（存量规则/设备换型）：只提示，**不清空**用户已有的动作与参数。 -->
              <p
                v-if="helpers.isEmpty(action)"
                class="hint-block"
                :data-test="`action-id-empty-hint-${index}`"
              >
                该设备暂无可用动作（可能是设备离线或驱动未声明）
              </p>
              <p
                v-else-if="helpers.allUnavailable(action)"
                class="hint-block"
                :data-test="`action-id-unavailable-hint-${index}`"
              >
                该设备的 {{ helpers.options(action).length }} 个动作当前均不可用，可先配置，执行结果由后端判定
              </p>
              <p
                v-else-if="helpers.missing(action)"
                class="hint-block action-card__hint--warn"
                :data-test="`action-id-missing-hint-${index}`"
              >
                动作不在当前设备目录中（存量规则或设备换型）：保留原值，保存时由后端校验
              </p>
            </dd>
          </div>
          <div class="span2">
            <dt>参数</dt>
            <dd>
              <span class="mono" data-test="action-params-summary">{{ paramsSummary(action.action_params_json) }}</span>
              <!-- 表单之外的「高级」出口：schema 之外的方言键在表单里没有位置，
                   而且表单在不完整时禁提交，用户有时就是要改原文里的一个字符。
                   只在**确实有 schema**（默认走表单）时出现，否则「编辑参数」本身就是 JSON。 -->
              <el-button
                v-if="helpers.hasSchema(action)"
                link
                type="primary"
                size="small"
                class="action-card__json-link"
                :data-test="`action-params-form-link-${index}`"
                :aria-label="`第 ${index + 1} 个动作改用 JSON 编辑参数`"
                @click="emit('open-json-params', action)"
              >手写 JSON</el-button>
            </dd>
          </div>
        </dl>
        <!-- 风险原因块：与徽标并存的第二条非颜色通道。后端 risk_reason 尚未实现
             （设计稿 §9 O-2），此处**如实说明原因文案未下发**，不编造危险说明
             —— 安全文案不得由前端想象（编造的"危险说明"比没有更危险）。 -->
        <div v-if="helpers.badge(action)" class="risk-reason" data-test="risk-reason">
          <span class="risk-reason__icon" aria-hidden="true">⚠</span>
          <span>
            <span class="risk-reason__label">风险原因：</span>
            {{ helpers.reason(action) }}
          </span>
        </div>
      </li>
    </ul>
  </el-form-item>
</template>

<script setup lang="ts">
import type { EdgeDevice } from '@/api/edgeDevice'
import { NORMAL_BADGE, paramsSummary, type ActionDraft } from '../actionList'
import type { ActionCardHelpers } from '../composables/useRuleActions'

/**
 * 多动作卡片列表（2026-11-12 第二轮表单区拆分，纯搬移）。
 *
 * 边界：只渲染 + 抛语义事件，**不重写任何判定**。风险徽标、动作名、候选动作、
 * 三种目录提示与"有没有参数表单"全部由 helpers 提供（同一份判定同时被保存前
 * 预检、参数弹窗与保存校验使用）—— 若在子组件里重算，就会出现"卡片说普通、
 * 预检说风险"这类安全信息分裂。
 *
 * 动作草稿是页面表单模型里的**同一批对象**（不复制）：v-model 写的是同一份数据，
 * 增删/排序走事件（数组引用由页面替换，见 moveAction 的说明）。
 */
defineProps<{
  actions: ActionDraft[]
  devices: EdgeDevice[]
  /** 风险动作数量（页面按同一份目录推导；这里只用于提示文案）。 */
  riskyCount: number
  helpers: ActionCardHelpers
}>()

const emit = defineEmits<{
  (e: 'add'): void
  (e: 'remove', uid: number): void
  (e: 'move', uid: number, delta: number): void
  (e: 'type-change', action: ActionDraft): void
  (e: 'device-change', action: ActionDraft): void
  (e: 'action-id-change', action: ActionDraft): void
  (e: 'open-params', uid: number): void
  (e: 'open-json-params', action: ActionDraft): void
}>()
</script>

<style scoped>
.mono { font-family: monospace; }
.text-muted { color: var(--el-text-color-secondary); }
.hint-block { margin-top: 4px; font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6; }

/* ============================================================================
   多动作卡片列表 (设计稿 §5 R7-R8 / §6 D-3 D-6)
   ========================================================================== */
.action-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 10px;
  flex-wrap: wrap;
}
.action-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 10px; }
.action-card {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  padding: 12px;
  background: var(--el-bg-color);
}
/* 风险卡片的**第一条非颜色通道**：左侧红边 + 浅红底（徽标与原因块是另外两条）。
   四重编码见设计 D-6：图标 / 文字 / 徽标色 / 卡片边。 */
.action-card.is-risk {
  border-color: var(--el-color-danger-light-5);
  border-left: 3px solid var(--el-color-danger);
  background: var(--el-color-danger-light-9);
}
.action-card__head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.action-card__index {
  display: inline-flex; align-items: center; justify-content: center;
  width: 20px; height: 20px; border-radius: 50%;
  background: var(--el-color-primary-light-9); color: var(--el-color-primary);
  font-size: 12px; font-weight: 600; flex: 0 0 auto;
}
.action-card__name { font-size: 14px; font-weight: 600; color: var(--el-text-color-primary); }
.action-card__ops { margin-left: auto; display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.risk-badge { font-weight: 600; }
.normal-badge, .unknown-badge { flex: 0 0 auto; }
.action-meta { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 16px; margin: 10px 0 0; }
.action-meta > div { display: flex; gap: 6px; min-width: 0; align-items: center; }
.action-meta > div.span2 { grid-column: 1 / -1; }
.action-meta dt { flex: 0 0 auto; color: var(--el-text-color-secondary); font-size: 12px; }
.action-meta dd { margin: 0; font-size: 12px; color: var(--el-text-color-regular); overflow-wrap: anywhere; flex: 1; min-width: 0; }
.action-meta dd :deep(.el-select) { width: 100%; }
/* 参数行：「手写 JSON」是表单之外的高级出口，与摘要同排但不抢主按钮的位置 */
.action-card__json-link { margin-left: 8px; }
/* 兼容态提示（存量规则 / 设备换型：动作不在当前目录里）。警告色小字 = 只是提示，
   不阻断保存 —— 判定权在后端，前端替它拦下来只会让合法规则存不进去。 */
.action-card__hint--warn { color: var(--el-color-warning); }
/* 风险原因块：第二条非颜色通道（与徽标并存的文本块）。 */
.risk-reason {
  display: flex; gap: 8px; align-items: flex-start; margin-top: 10px;
  padding: 8px 10px; border-left: 3px solid var(--el-color-danger);
  border-radius: 4px; background: var(--el-bg-color);
  font-size: 12px; color: var(--el-text-color-regular); line-height: 1.6;
}
.risk-reason__icon { color: var(--el-color-danger); font-weight: 700; flex: 0 0 auto; }
.risk-reason__label { font-weight: 600; color: var(--el-color-danger); }

/* ============================================================================
   窄屏：768px 主断点（与 theme.css / useResponsive 同源）
   ========================================================================== */
@media (max-width: 768px) {
  .action-meta { grid-template-columns: 1fr; }
}

/* ============================================================================
   窄屏：480px 加强档 (F-10；设计稿 §7)
   ========================================================================== */
@media (max-width: 480px) {
  /* 动作卡操作区换行到第二行、右对齐，并补足触控高度（theme.css 的 36px/44px 口径）。 */
  .action-card__head { align-items: flex-start; }
  .action-card__name { flex: 1 1 100%; }
  .action-card__ops { margin-left: 0; width: 100%; justify-content: flex-end; gap: 2px; }
  .action-card__ops :deep(.el-button) { min-height: 36px; }
  /* dt/dd 上下堆叠：避免"风险原因"这类长标签挤压正文。 */
  .action-meta > div { flex-direction: column; gap: 0; align-items: stretch; }
}
</style>
