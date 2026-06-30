<template>
  <div class="bms-mos-status">
    <div class="mos-item" :class="{ on: chargeOn }">
      <el-icon :size="20" :color="chargeOn ? THEME_COLORS.success : THEME_COLORS.info">
        <component :is="chargeOn ? Open : Lock" />
      </el-icon>
      <div class="mos-info">
        <span class="mos-label">充电MOS</span>
        <el-tag size="small" :type="chargeOn ? 'success' : 'info'" effect="dark">
          {{ chargeOn ? 'ON' : 'OFF' }}
        </el-tag>
      </div>
    </div>
    <div class="mos-item" :class="{ on: dischargeOn }">
      <el-icon :size="20" :color="dischargeOn ? THEME_COLORS.success : THEME_COLORS.info">
        <component :is="dischargeOn ? Open : Lock" />
      </el-icon>
      <div class="mos-info">
        <span class="mos-label">放电MOS</span>
        <el-tag size="small" :type="dischargeOn ? 'success' : 'info'" effect="dark">
          {{ dischargeOn ? 'ON' : 'OFF' }}
        </el-tag>
      </div>
    </div>
    <div class="mos-item" v-if="balanceOn" :class="{ on: true }">
      <el-icon :size="20" :color="THEME_COLORS.success">
        <Open />
      </el-icon>
      <div class="mos-info">
        <span class="mos-label">均衡</span>
        <el-tag size="small" type="success" effect="dark">ON</el-tag>
      </div>
    </div>
    <div class="mos-item" v-if="!hasFetStatus" style="opacity: 0.6;">
      <el-icon :size="20" :color="THEME_COLORS.info"><Lock /></el-icon>
      <div class="mos-info">
        <span class="mos-label">无FET状态数据</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Open, Lock } from '@element-plus/icons-vue'
import { THEME_COLORS } from '@/utils/theme'

const props = defineProps<{
  data: Record<string, number> | null
}>()

// fet_status bitmask: bit0 = charge MOS, bit1 = discharge MOS
// value 0 = both OFF, 1 = charge ON, 2 = discharge ON, 3 = both ON
const hasFetStatus = computed(() => {
  if (!props.data) return false
  return props.data.fet_status !== undefined || props.data.mos_charge !== undefined
})

const fetStatus = computed(() => {
  if (!props.data) return 0
  return props.data.fet_status ?? 0
})

const chargeOn = computed(() => {
  if (!props.data) return false
  // Check fet_status bitmask first, then fallback to explicit fields
  if (props.data.fet_status !== undefined) return (props.data.fet_status & 1) !== 0
  return props.data.mos_charge === 1 || props.data.mos_charge_on === 1 || props.data.charge_mos === 1
})

const dischargeOn = computed(() => {
  if (!props.data) return false
  if (props.data.fet_status !== undefined) return (props.data.fet_status & 2) !== 0
  return props.data.mos_discharge === 1 || props.data.mos_discharge_on === 1 || props.data.discharge_mos === 1
})

const balanceOn = computed(() => {
  if (!props.data) return false
  return props.data.balancing === 1 || props.data.balance === 1 || props.data.mos_balance === 1
})
</script>

<style scoped>
.bms-mos-status {
  display: flex;
  gap: 24px;
  padding: 8px 0;
  flex-wrap: wrap;
}
.mos-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 16px;
  background: var(--el-fill-color-lighter);
  border-radius: 8px;
  border: 2px solid transparent;
  min-width: 120px;
}
.mos-item.on {
  background: var(--el-color-success-light-9);
  border-color: var(--el-color-success-light-5);
}
.mos-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.mos-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
