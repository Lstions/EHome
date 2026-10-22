<template>
  <div class="time-range-selector">
    <el-radio-group v-model="timeRange" size="small" @change="handleChange">
      <el-radio-button value="1h">1小时</el-radio-button>
      <el-radio-button value="24h">24小时</el-radio-button>
      <el-radio-button value="7d">7天</el-radio-button>
      <el-radio-button v-if="showCustom" value="custom">自定义</el-radio-button>
    </el-radio-group>
    <el-date-picker
      v-if="timeRange === 'custom'"
      v-model="customTimeRange"
      type="datetimerange"
      size="small"
      range-separator="至"
      start-placeholder="开始时间"
      end-placeholder="结束时间"
      format="YYYY-MM-DD HH:mm"
      value-format="YYYY-MM-DD HH:mm:ss"
      class="custom-date-picker"
      @change="emit('change')"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  modelValue: string
  customRange: [string, string] | null
  /** 是否提供「自定义」区间（仪表盘只有 1h/24h/7d，传 false） */
  showCustom?: boolean
}>(), { showCustom: true })

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:customRange': [value: [string, string] | null]
  change: []
}>()

const timeRange = computed({
  get: () => props.modelValue,
  set: (val: string) => emit('update:modelValue', val),
})

const customTimeRange = computed({
  get: () => props.customRange,
  set: (val: [string, string] | null) => emit('update:customRange', val),
})

const handleChange = () => {
  emit('change')
}
</script>

<style scoped>
/*
  时间档选择器（1小时 / 24小时 / 7天 / 自定义）—— **图表卡工具条档**的唯一实现。

  2026-10-22 用户第七轮真机反馈「按钮大小比例与图表相比大太多了，按钮大小没有统一的规范」。
  根因不是某一个值写错，而是同一张图表卡里并存三档高度（时间档 40/44、导出 36/44、分组 tab 30/32）
  与三档字号（13/14/15）；小屏上卡头折 3~4 行、控件块高 172~230px，而图只有 200px
  ⇒ 控件块/图高 = 0.90~1.20。基线证据：.logs/ui-fix/round7/before-summary.json。

  修法（规范 §4.4「控件尺寸档位」）：图表卡工具条**全站只此一档**，与导出按钮、分组 tab
  共用 theme.css 的 --control-* token：
    · 视觉高：桌面（fine ≥769px）32px；≤768px 或 pointer:coarse 36px；
    · 字号恒 12px；圆角 6px；文字型控件横向内距恒 10px。
  三者高度严格相等（同一 token），不再出现 40/36/32 并存。
  36px 为何低于正文控件的 44px：图表卡工具条属规范 §4.4 第 5 条
  「极高密度、非主要任务的工具栏」例外，条件是相邻间距 ≥6px 且每个控件有可访问名称 ——
  本组件是纯文字档位（原生 radio-button），名称由文字承担。

  ⚠ 不再有 dense / compact 第二档（2026-10-21 已否决：同一控件两种尺寸正是本缺陷的同源问题）。
  ⚠ 分段控件保持「连体」：只给首尾段外侧 6px 圆角，中间段不做出 4 个孤立圆角。
*/
.time-range-selector {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}

/*
  四段**等宽**（任意视口）：EP 的 radio-button 是按内容宽度排的，
  实测 1440 下 1小时/24小时/7天/自定义 = 57/64/44/63px —— 长短不齐看起来像没对齐，
  这也正是用户反馈「时间样式不对」的一部分。
  改用 grid-auto-columns:1fr：所有列强制等宽（取最宽标签），桌面不拉伸铺满、窄屏再由下方规则铺成整行。
*/
.time-range-selector :deep(.el-radio-group) {
  display: grid;
  grid-auto-flow: column;
  grid-auto-columns: 1fr;
  border-radius: var(--control-radius-toolbar, 6px);
}

.time-range-selector :deep(.el-radio-button) {
  min-width: 0;
}

.time-range-selector :deep(.el-radio-button__inner) {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  /* 视觉高与 theme.css --control-h-toolbar 同源（导出按钮 / 分组 tab 用同一 token） */
  height: var(--control-h-toolbar, 32px);
  min-height: var(--control-h-toolbar, 32px);
  box-sizing: border-box;
  padding: 0 10px;
  font-size: var(--control-font-toolbar, 12px);
  line-height: 1;
}

/* 分段控件整体圆角 6px：首/尾段外侧取圆角，中间段方角，保持连体分段语义 */
.time-range-selector :deep(.el-radio-button:first-child .el-radio-button__inner) {
  border-top-left-radius: var(--control-radius-toolbar, 6px);
  border-bottom-left-radius: var(--control-radius-toolbar, 6px);
}
.time-range-selector :deep(.el-radio-button:last-child .el-radio-button__inner) {
  border-top-right-radius: var(--control-radius-toolbar, 6px);
  border-bottom-right-radius: var(--control-radius-toolbar, 6px);
}

/*
  窄屏 / 粗指针：工具条档的触摸尺寸 36px（§4.4 第 5 条例外档）。
  必须用组件级规则（编译后 0,3,0）压过 theme.css 的既有约束：
    · 基础 .el-radio-button__inner{min-height:36px}（0,1,0）—— 桌面档要 32px；
    · 粗指针 .el-radio-button__inner{min-height:44px}（0,1,0）—— 本例外档取 36px。
  这正是「例外写在组件作用域内」而不是改全局 44px 档的原因：
  正文输入/页头/表格的 44px 档不受影响（theme.css 一字未动）。
*/

.custom-date-picker {
  width: 340px;
}

@media (max-width: 768px) {
  /*
    2026-10-21 用户反馈「按钮与图表大小对比比例异常大」（窄屏历史数据卡）：
    不再把选择器强制 width:100% + 四段 flex:1 铺满整行（旧实现 393~659px 下
    四段各 90~150px、高 40px，比旁边的图还抢眼）；只保留「等宽」。
  */
  .time-range-selector {
    width: auto;
    max-width: 100%;
  }
  .time-range-selector :deep(.el-radio-button) {
    flex: 1 1 0;
    min-width: 0;
  }
  .time-range-selector :deep(.el-radio-button__inner) {
    width: 100%;
    height: var(--control-h-toolbar-touch, 36px);
    min-height: var(--control-h-toolbar-touch, 36px);
    padding: 0 10px;
    font-size: var(--control-font-toolbar, 12px);
    white-space: nowrap;
  }
  .custom-date-picker {
    width: 100% !important;
  }
}

@media (pointer: coarse) {
  .time-range-selector :deep(.el-radio-button__inner) {
    height: var(--control-h-toolbar-touch, 36px);
    min-height: var(--control-h-toolbar-touch, 36px);
  }
}
</style>