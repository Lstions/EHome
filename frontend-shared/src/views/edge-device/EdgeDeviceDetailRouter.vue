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
import { ref, defineAsyncComponent, watch, shallowRef, markRaw } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useEdgeDeviceStore } from '@/stores/edgeDevice'

const route = useRoute()
const edgeDeviceStore = useEdgeDeviceStore()
const deviceType = ref<string>('')

// Lazy-load page components. Each is code-split.
const BmsDetailPage = defineAsyncComponent(() => import('./bms/BmsDetailPage.vue'))
const InverterDetailPage = defineAsyncComponent(() => import('./inverter/InverterDetailPage.vue'))
const GenericDeviceDetail = defineAsyncComponent(() => import('./GenericDeviceDetail.vue'))

// device_type → dedicated page mapping
const componentMap: Record<string, any> = {
  jiabaida_bms: BmsDetailPage,
  battery: BmsDetailPage,        // battery shares BMS page (simplified)
  inverter: InverterDetailPage,
}

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
    targetComponent.value = markRaw(componentMap[dev.device_type] || GenericDeviceDetail)
  } catch {
    if (sequence !== resolveSequence) return
    error.value = true
    ElMessage.error('获取设备信息失败，请检查网络或稍后重试')
  } finally {
    if (sequence === resolveSequence) loading.value = false
  }
}

// Resolve on mount and when route id changes
resolveComponent()
watch(() => route.params.id, () => resolveComponent())
</script>
