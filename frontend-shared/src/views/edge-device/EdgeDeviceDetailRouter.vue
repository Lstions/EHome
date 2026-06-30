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
    <component v-else-if="targetComponent" :is="targetComponent" />
  </div>
</template>

<script setup lang="ts">
import { ref, defineAsyncComponent, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { edgeDeviceApi } from '@/api/edgeDevice'

const route = useRoute()
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

const targetComponent = ref<any>(null)
const loading = ref(false)
const error = ref(false)

async function resolveComponent() {
  const id = Number(route.params.id)
  if (!id) return

  loading.value = true
  error.value = false
  targetComponent.value = null

  try {
    const dev = await edgeDeviceApi.getDetail(id)
    deviceType.value = dev.device_type
    targetComponent.value = componentMap[dev.device_type] || GenericDeviceDetail
  } catch {
    error.value = true
    ElMessage.error('获取设备信息失败，请检查网络或稍后重试')
  } finally {
    loading.value = false
  }
}

// Resolve on mount and when route id changes
resolveComponent()
watch(() => route.params.id, () => resolveComponent())
</script>
