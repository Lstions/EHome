<!--
  诊断类区块的折叠容器（2026-10-20 移动端改版 §3）。

  为什么用渲染函数而不是 <template>：
    桌面端必须「零包装」——直接把插槽内容返回，DOM 与改前逐字节一致。
    父页有大量 `.row > .card` / `.stream-poll > .stream-card` 这类**直接子选择器**，
    只要多一层 div（哪怕 display:contents）这些选择器就全部失配，桌面布局会塌。
    display:contents 只解决 flex 布局参与，解决不了选择器匹配。

    窄屏才渲染折叠头：标题行 = caret + 区块名 + 一行关键结论（§3.2 折叠不是隐藏信息）；
    正文用内联 style 的 display:none 收起（不用 v-if：图表组件要保活，避免展开时重新取数/重新 init）。
-->
<script lang="ts">
import { defineComponent, h, ref, watch } from 'vue'
import { ArrowRight } from '@element-plus/icons-vue'

export default defineComponent({
  name: 'CollapseBlock',
  props: {
    /** 区块 id，用于折叠记忆（localStorage 里存的就是它） */
    blockId: { type: String, required: true },
    title: { type: String, required: true },
    /** 折叠态一行关键结论；为空时不占位 */
    summary: { type: String, default: '' },
    /** 是否启用折叠（窄屏 true；桌面 false 时零包装直出插槽） */
    collapsible: { type: Boolean, default: false },
    /** 当前是否展开 */
    open: { type: Boolean, default: false },
    /**
     * 懒挂载（契约 §2.4）：collapsible && lazy 时，正文在**首次展开前不渲染**，
     * 首次展开后保活（不再卸载）。默认 false = 现在的行为（收起时 display:none 保活）。
     *
     * 为什么需要：窄屏 7 个诊断区块默认收起，但 children（图表卡）在首屏就挂载并取数 ——
     * 用户根本看不到的 protection 30 天 / 温度 / SOC 三张卡照样发出 3 个重查询（根因 R3）。
     * 桌面 collapsible=false 时本 prop 不生效（零包装直出插槽，布局与改前一致）。
     */
    lazy: { type: Boolean, default: false },
  },
  emits: ['toggle'],
  setup(props, { slots, emit }) {
    const bodyId = 'collapse-body-' + props.blockId
    /** 是否已经历过「首次展开」—— lazy 下决定正文是否已挂载（一旦挂载不再卸载） */
    const openedOnce = ref(props.open)
    watch(
      () => props.open,
      (open) => {
        if (open) openedOnce.value = true
      },
    )
    return () => {
      // 桌面：不产生任何节点，插槽内容直接成为父容器的子节点
      if (!props.collapsible) return slots.default ? slots.default() : null

      const head = h(
        'button',
        {
          type: 'button',
          class: 'collapse-head',
          'aria-expanded': props.open,
          'aria-controls': bodyId,
          onClick: () => emit('toggle'),
        },
        [
          h(ArrowRight, { class: ['collapse-caret', props.open ? 'open' : ''] }),
          h('span', { class: 'collapse-title' }, props.title),
          props.summary ? h('span', { class: 'collapse-summary' }, props.summary) : null,
        ],
      )
      // lazy && 尚未展开过：只留一个空的 body 占位（保住 aria-controls 的指向），不创建子组件。
      // 兜底 || props.open：watch 是 pre-flush，理论上先于本次渲染，但首帧展开不依赖 flush 更稳。
      const bodyRendered = !props.lazy || openedOnce.value || props.open
      const body = h(
        'div',
        { id: bodyId, class: 'collapse-body', style: props.open ? undefined : { display: 'none' } },
        bodyRendered && slots.default ? slots.default() : [],
      )
      return h(
        'div',
        { class: ['collapse-block', 'is-collapsible', props.open ? 'is-open' : ''] },
        [head, body],
      )
    }
  },
})
</script>

<style scoped>
.collapse-block {
  display: block;
  min-width: 0;
  margin-bottom: 12px;
  /* 作为 `.row` 的 flex 子项必须占满整行：默认 flex:0 1 auto 会收缩到「标题+摘要」的内容宽，
     实测「操作历史」只有 157px、「实时数据流」178px（父容器 321px）—— 半屏宽的折叠行，
     展开后里面的卡片也跟着半屏。父页的 .diag-row 规则只覆盖了新诊断卡，这两个既有区块没有。 */
  flex: 1 1 100%;
}
.collapse-block:last-child {
  margin-bottom: 0;
}

/* 折叠头即触摸目标：整行 44px 可点 */
.collapse-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-height: 44px;
  padding: 8px 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: var(--radius-sm);
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-primary);
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}
.collapse-head:hover {
  background: var(--el-fill-color-light);
}
.collapse-block.is-open .collapse-head {
  border-bottom-left-radius: 0;
  border-bottom-right-radius: 0;
}
.collapse-caret {
  flex: 0 0 auto;
  /* 显式尺寸：@element-plus/icons-vue 的 svg 没有 width/height 属性，
     作为 flex 子项时按替换元素默认 300×150 撑开，会把折叠头撑到 150px+。 */
  width: 14px;
  height: 14px;
  color: var(--el-text-color-secondary);
  transition: transform 0.2s;
}
.collapse-caret.open {
  transform: rotate(90deg);
}
.collapse-title {
  flex: 0 0 auto;
  font-weight: 600;
  white-space: nowrap;
}
/* 摘要：折叠时是「这一块现在怎么样」的唯一线索，必须完整可读（不 ellipsis） */
.collapse-summary {
  flex: 1 1 auto;
  min-width: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  text-align: right;
  overflow-wrap: anywhere;
}
.collapse-body {
  min-width: 0;
}
</style>