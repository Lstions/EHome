<template>
  <div>
    <!-- Loading state -->
    <el-card v-if="loading">
      <el-skeleton :rows="4" animated />
    </el-card>

    <!-- Error state -->
    <el-card v-else-if="error">
      <el-result icon="warning" title="加载失败" sub-title="无法获取设备信息，请重试">
        <template #extra>
          <el-button type="primary" @click="resolveComponent">重试</el-button>
        </template>
      </el-result>
    </el-card>

    <!-- Success: render the resolved component -->
    <component v-else-if="targetComponent" :is="targetComponent" :key="route.params.id" :data-detail-key="String(route.params.id)" />
  </div>
</template>

<script setup lang="ts">
import { ref, shallowRef, defineAsyncComponent, watch } from 'vue'
import { feedback } from '@/utils/feedback'
import { useRoute } from 'vue-router'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'

const route = useRoute()
const edgeDeviceStore = useEdgeDeviceStore()
const deviceType = ref<string>('')

// Lazy-load page components. Each is code-split.
const BmsDetailPage = defineAsyncComponent(() => import('./bms/BmsDetailPage.vue'))
const InverterDetailPage = defineAsyncComponent(() => import('./inverter/InverterDetailPage.vue'))
const GenericDeviceDetail = defineAsyncComponent(() => import('./GenericDeviceDetail.vue'))

/**
 * device_type → 专用详情页映射。
 *
 * ⚠ key 必须是**后端驱动的注册名**（日志 "Driver registered: <name>"、
 * edge_devices.device_type 列），不是人类可读的分类名。
 *
 * 2026-10-18 实测缺陷：这里原先把逆变器写成 `inverter`，而驱动注册名是
 * `techfine_inverter` ⇒ 映射永远 miss ⇒ 逆变器设备**一直走 GenericDeviceDetail**，
 * 那个 950 行的专用页面（功率流/MPPT/告警/发电量）从来没被渲染过。
 * 症状很容易误判成"页面数据没接上"，实际是路由根本没选中它。
 *
 * 因此对每个 key 都注明来源；新增驱动时**必须**同步此表，否则会静默退化为通用页。
 */
const componentMap: Record<string, any> = {
  // backend/internal/drivers/jiabaida*.go → "Driver registered: jiabaida_bms"
  jiabaida_bms: BmsDetailPage,
  battery: BmsDetailPage,        // battery shares BMS page (simplified)
  // backend/internal/drivers/inverter_techfine.go → "Driver registered: techfine_inverter"
  techfine_inverter: InverterDetailPage,
  // 兼容：早期数据/手工建库可能写成不带厂商前缀的名字
  inverter: InverterDetailPage,
}

/*
 * ⚠ 必须用 shallowRef 而不是 ref：
 * ref() 会对赋值对象做深度 reactive 包装，而 defineAsyncComponent() 返回的是
 * **组件**（带 __asyncLoader 等字段的对象）。被 reactive 包装后交给 <component :is>
 * 时 Vue 会打出 "Vue received a Component that was made a reactive object" 警告，
 * 且包装过程会遍历组件对象带来无谓开销（控制台已实测到该警告）。
 * shallowRef 只存引用、不做深度代理，是"持有组件"的正确容器。
 */
const targetComponent = shallowRef<any>(null)
const loading = ref(false)
const error = ref(false)
let resolveSequence = 0

async function resolveComponent() {
  const sequence = ++resolveSequence
  const id = Number(route.params.id)
  loading.value = true
  error.value = false
  targetComponent.value = null
  if (!id) {
    loading.value = false
    error.value = true
    return
  }

  try {
    const dev = await edgeDeviceStore.fetchDetail(id, true)
    if (sequence !== resolveSequence || Number(route.params.id) !== id) return
    deviceType.value = dev.device_type
    targetComponent.value = componentMap[dev.device_type] || GenericDeviceDetail
  } catch {
    if (sequence !== resolveSequence) return
    error.value = true
    feedback.error('获取设备信息失败，请检查网络或稍后重试')
  } finally {
    if (sequence === resolveSequence) loading.value = false
  }
}

// Resolve on mount and when route id changes
resolveComponent()
watch(() => route.params.id, () => resolveComponent())
</script>
