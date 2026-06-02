<template>
  <span ref="numberRef" class="count-up" :class="{ loading: isAnimating }">
    <slot :value="displayValue">{{ formattedValue }}</slot>
  </span>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'

const props = withDefaults(defineProps<{
  value: number
  duration?: number
  decimals?: number
  prefix?: string
  suffix?: string
  separator?: string
  startImmediately?: boolean
}>(), {
  duration: 1000,
  decimals: 0,
  prefix: '',
  suffix: '',
  separator: ',',
  startImmediately: true
})

const emit = defineEmits<{
  (e: 'complete'): void
}>()

const displayValue = ref(0)
const isAnimating = ref(false)
const animationFrame = ref<number | null>(null)
const startTime = ref<number | null>(null)

// 格式化数字
const formattedValue = computed(() => {
  const value = displayValue.value.toFixed(props.decimals)
  const parts = value.split('.')
  parts[0] = parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, props.separator)
  return `${props.prefix}${parts.join('.')}${props.suffix}`
})

// 缓动函数
const easeOutCubic = (t: number): number => {
  return 1 - Math.pow(1 - t, 3)
}

// 动画函数
const animate = (timestamp: number) => {
  if (!startTime.value) startTime.value = timestamp
  
  const elapsed = timestamp - startTime.value
  const progress = Math.min(elapsed / props.duration, 1)
  const easedProgress = easeOutCubic(progress)
  
  displayValue.value = easedProgress * props.value
  
  if (progress < 1) {
    animationFrame.value = requestAnimationFrame(animate)
  } else {
    displayValue.value = props.value
    isAnimating.value = false
    emit('complete')
  }
}

// 开始动画
const startAnimation = () => {
  if (animationFrame.value) {
    cancelAnimationFrame(animationFrame.value)
  }
  
  startTime.value = null
  displayValue.value = 0
  isAnimating.value = true
  animationFrame.value = requestAnimationFrame(animate)
}

// 重置
const reset = () => {
  if (animationFrame.value) {
    cancelAnimationFrame(animationFrame.value)
  }
  displayValue.value = 0
  isAnimating.value = false
}

// 监听值变化
watch(() => props.value, (newVal, oldVal) => {
  if (newVal !== oldVal) {
    startAnimation()
  }
})

onMounted(() => {
  if (props.startImmediately && props.value > 0) {
    startAnimation()
  }
})

onUnmounted(() => {
  if (animationFrame.value) {
    cancelAnimationFrame(animationFrame.value)
  }
})

// 暴露方法
defineExpose({
  start: startAnimation,
  reset
})
</script>

<style scoped>
.count-up {
  display: inline-block;
  font-variant-numeric: tabular-nums;
  transition: color 0.3s;
}

.count-up.loading {
  color: #409eff;
}
</style>
