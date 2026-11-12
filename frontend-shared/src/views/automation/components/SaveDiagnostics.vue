<template>
  <!-- 保存前诊断（§3.3）：位置**紧跟 footer 之上** —— 用户刚点完「保存」，视线就在这里，
       放在表单中部会被误读成"某个字段的报错"。默认不渲染，只在本轮 precheck
       返回 blocking_errors / warnings 时出现，且每次 onSave 整体重算（不累积上次残留）。 -->
  <div
    v-if="blockingErrors.length || warnings.length"
    class="save-diagnostics"
    data-test="save-diagnostics"
  >
    <!-- 阻断：硬校验，修正前不允许保存。必须逐条列出全部条目 ——
         改前是 ElMessage.warning(blocking[0])，多条错误要来回保存 N 次。
         role="alert" 让读屏器在诊断区出现时立即播报，避免焦点仍在按钮上而漏读。 -->
    <el-alert
      v-if="blockingErrors.length"
      type="error"
      role="alert"
      :closable="false"
      show-icon
      :title="`保存被拒绝（${blockingErrors.length} 项）`"
      data-test="save-blocking-errors"
    >
      <p class="save-diagnostics__hint">这些是硬校验，修正后才能保存</p>
      <ul class="save-diagnostics__list">
        <li v-for="(message, index) in blockingErrors" :key="`blocking-${index}`">{{ message }}</li>
      </ul>
    </el-alert>
    <!-- 风险提示：后端判定"不阻塞保存，但保存后可能永不触发"。第一次点击只展示，
         主按钮改语义为「我已了解，仍然保存」；第二次点击（同一份内容）才落库。 -->
    <el-alert
      v-if="warnings.length"
      type="warning"
      role="alert"
      :closable="false"
      show-icon
      :title="`以下问题可能导致规则永不触发（${warnings.length} 项）`"
      data-test="save-warnings"
    >
      <ul class="save-diagnostics__list">
        <li v-for="(message, index) in warnings" :key="`warning-${index}`">{{ message }}</li>
      </ul>
    </el-alert>
  </div>
</template>

<script setup lang="ts">
/**
 * 保存前诊断区（2026-11-12 第二轮表单区拆分，纯搬移）。
 *
 * 边界：**只渲染**，不持有任何状态。诊断的两个清单（blocking_errors / warnings）
 * 与"已确认的 warnings 指纹"都属于保存流程（onSave 的判定与二次确认语义），
 * 留在页面 —— 子组件只接 props，避免"展示"与"放行判定"出现两份口径。
 *
 * 为什么搬：它是表单里唯一一段"只读展示、与表单模型无关"的模板，
 * 搬出去后页面的保存流程（precheck → 诊断 → 风控清单 → 提交）无需 diff 外部组件。
 */
defineProps<{
  /** 硬校验清单（blocking_errors）。非空 = 本次保存被拒，逐条列出。 */
  blockingErrors: string[]
  /** 风险提示（warnings）。非空 = 保存后**可能**永不触发，需要用户二次确认。 */
  warnings: string[]
}>()
</script>

<style scoped>
/* ============================================================================
   保存前诊断区 (§3.3)：只做间距与列表排版，不引入新视觉语言 ——
   颜色/图标全部沿用 el-alert 的 error/warning 语义（红=硬阻断、黄=风险提示）。
   ========================================================================== */
.save-diagnostics { margin-top: 12px; display: flex; flex-direction: column; gap: 8px; }
.save-diagnostics__hint { margin: 0 0 4px; font-size: 12px; line-height: 1.6; }
.save-diagnostics__list { margin: 0; padding-left: 18px; font-size: 12px; line-height: 1.7; overflow-wrap: anywhere; }
</style>
