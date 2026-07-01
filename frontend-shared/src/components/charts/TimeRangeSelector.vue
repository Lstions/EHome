<template>
  <div class="time-range-selector">
    <el-radio-group v-model="timeRange" size="small" @change="handleChange">
      <el-radio-button value="1h">1小时</el-radio-button>
      <el-radio-button value="24h">24小时</el-radio-button>
      <el-radio-button value="7d">7天</el-radio-button>
      <el-radio-button value="custom">自定义</el-radio-button>
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
      style="margin-left: 10px; width: 340px;"
      @change="emit('change')"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  modelValue: string
  customRange: [string, string] | null
}>()

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
.time-range-selector {
  display: flex;
  align-items: center;
}
</style>
