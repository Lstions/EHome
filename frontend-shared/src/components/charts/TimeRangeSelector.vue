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
  时间档选择器（1小时 / 24小时 / 7天 / 自定义）统一样式。

  2026-10-21 用户真机反馈「时间样式不对、文字没居中、大小也不对」，实测根因：
    ① 全局 theme.css 给 .el-radio-button__inner 抬了 min-height:36px（触摸目标），
       但没改 display/line-height ⇒ 盒子被撑高、文字仍贴上沿（看起来没居中）；
    ② 调用处（BMS 图表卡）又各自 :deep 覆盖成 24px ⇒ 同一控件在不同卡片不同高；
    ③ 窄屏 flex:1 1 auto 让四段按内容分配（实测 53/59/41/58px）⇒ 长短不齐像没对齐；
    ④ 字号 12px 与相邻正文 13px 不一致，看起来「大小不对」。
  在组件内一次性收口：inline-flex 垂直居中 + 统一 13px + 窄屏四段等宽 + 触摸 44px。
*/
.time-range-selector {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}

.time-range-selector :deep(.el-radio-button__inner) {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 36px;
  padding: 0 12px;
  font-size: 13px;
  line-height: 1;
}

/*
  关于「紧凑档」：**已废除**（2026-10-21 独立复核）。
  原 dense 档把卡片头里的分段按钮压到 24px/12px，与同页主控件 36px/13px 并存 ——
  这正是用户反馈「大小也不对」的同源问题：同一控件出现两种尺寸。
  复核实测更严重：dense 的特异性 0,3,0 压过窄屏/粗指针规则（0,2,0），
  真实手机（pointer:coarse, 393px）上触摸目标只有 24px，很难点中。
  现在全站只有一档尺寸：桌面 36px / 窄屏 40px / 粗指针 44px，字号恒 13px。
*/

.custom-date-picker {
  width: 340px;
}

@media (max-width: 768px) {
  .time-range-selector {
    width: 100%;
  }
  .time-range-selector :deep(.el-radio-group) {
    display: flex;
    width: 100%;
    flex-wrap: nowrap;
  }
  .time-range-selector :deep(.el-radio-button) {
    flex: 1 1 0;
    min-width: 0;
  }
  .time-range-selector :deep(.el-radio-button__inner) {
    width: 100%;
    min-height: 40px;
    padding: 0 4px;
    font-size: 13px;
    white-space: nowrap;
  }
  .custom-date-picker {
    width: 100% !important;
  }
}

@media (pointer: coarse) {
  .time-range-selector :deep(.el-radio-button__inner) {
    min-height: 44px;
  }
}
</style>