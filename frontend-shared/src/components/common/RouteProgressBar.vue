import { useRouteProgress } from '@/stores/routeProgress'

/**
 * 全局路由进度条（NProgress 风格顶部细条，零新依赖自实现）。
 *
 * 挂在 App.vue 根部；进度状态由 useRouteProgress() 模块级单例驱动，
 * router 全局守卫在 beforeEach/afterEach/onError 中调用 start/done/fail。
 *
 * 视觉约定：
 * - 2px 顶部细条，主色渐变 + 轻微光晕，处于最顶层（z-index 4000，高于 el-message-box 的 2001）。
 * - 非 busy 时整条以 opacity 0 隐藏（保留 transition 收尾动画）。
 * - 进度收满后淡出动画由 stores/routeProgress 内部的 250ms 延迟兜底。
 */

<script setup lang="ts">
import { useRouteProgress } from '@/stores/routeProgress'

const { progress, visible } = useRouteProgress()
</script>

<template>
  <div
    class="route-progress-bar"
    :class="{ 'is-visible': visible }"
    aria-hidden="true"
  >
    <div class="route-progress-bar__fill" :style="{ width: progress + '%' }" />
  </div>
</template>

<style scoped>
.route-progress-bar {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  height: 2px;
  z-index: 4000;
  pointer-events: none;
  opacity: 0;
  /* 淡入快、淡出慢：transition 声明跟随状态方向——
     基类（隐藏态）携带「淡出」过渡（0.25s），.is-visible（可见态）携带
     「淡入」过渡（0.15s）。CSS transition 按切换后的目标状态取值，
     因此出现时 0.15s 快进、收尾隐藏时 0.25s 慢出（配合 routeProgress
     内部 250ms 隐藏延迟）。旧写法把两个 transition 写在同一声明里，
     同属性后者整体胜出，淡入淡出都被 0.25s 覆盖，意图未生效。 */
  transition: opacity 0.25s ease-out;
}

.route-progress-bar.is-visible {
  opacity: 1;
  transition: opacity 0.15s ease-in;
}

.route-progress-bar__fill {
  height: 100%;
  width: 0;
  border-radius: 0 2px 2px 0;
  background: linear-gradient(90deg, var(--el-color-primary-light-5), var(--el-color-primary));
  box-shadow: 0 0 6px var(--el-color-primary);
  transition: width 0.2s ease-out;
}
</style>
