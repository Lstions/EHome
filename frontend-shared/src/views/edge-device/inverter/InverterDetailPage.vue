<template>
  <div class="inverter-detail">
    <DeviceHeader
      :device="device"
      :ws-connected="wsConnected"
      :syncing-h-a="syncingHA"
      :refreshing="refreshing"
      :title="device?.name || '逆变器详情'"
      @back="goBack"
      @sync-to-h-a="handleSyncToHA"
      @refresh="handleRefresh"
      @updated="fetchDeviceDetail"
    />

    <el-card v-if="loading && !device" shadow="hover">
      <el-skeleton :rows="4" animated />
    </el-card>
    <template v-else-if="device">
      <DeviceInfoCard :device="device" />

      <!-- Core metrics：统一共享组件 MetricStatCard（透明底彩色图标 + 辅助槽恒占位等高 + 充放电方向语义色） -->
      <el-row :gutter="20" style="margin-top: 20px;">
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard label="PV输入" :value="formatPower(totalPvPower)" tone="warning">
            <template #icon><el-icon :size="20"><Sunrise /></el-icon></template>
          </MetricStatCard>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <!--
            电池卡：显示电压 + 电量，方向由充/放电流两个独立字段判定。
            ⚠ 不能用 latestData.battery_current —— 后端没有这个字段，
            真实字段是 battery_charge_current / battery_discharge_current。
          -->
          <MetricStatCard
            label="电池"
            :value="batteryVoltageText"
            unit="V"
            tone="success"
            :direction="batteryDirection"
            :sub-text="`${formatNum(latestData?.battery_capacity, 0)}% · ${batteryCurrentText}`"
          >
            <template #icon><el-icon :size="20"><Lightning /></el-icon></template>
          </MetricStatCard>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <MetricStatCard
            label="电网"
            :value="formatNum(latestData?.grid_voltage, 0)"
            unit="V"
            tone="primary"
            :sub-text="`${formatNum(latestData?.grid_frequency, 1)}Hz`"
          >
            <template #icon><el-icon :size="20"><Connection /></el-icon></template>
          </MetricStatCard>
        </el-col>
        <el-col :xs="12" :sm="12" :md="6">
          <!-- 负载卡：用 output_active_power（有功，W）。若同时有视在功率则作为副文本。 -->
          <MetricStatCard
            label="负载"
            :value="formatPower(num(latestData?.output_active_power))"
            tone="info"
            :sub-text="loadSubText"
          >
            <template #icon><el-icon :size="20"><HomeFilled /></el-icon></template>
          </MetricStatCard>
        </el-col>
      </el-row>

      <!-- Power flow diagram -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header><span>功率流向</span></template>
        <InverterPowerFlow
          :pv-power="totalPvPower"
          :load-power="num(latestData?.output_active_power)"
          :battery-voltage="num(latestData?.battery_voltage)"
          :battery-charge-current="num(latestData?.battery_charge_current)"
          :battery-discharge-current="num(latestData?.battery_discharge_current)"
          :battery-capacity="num(latestData?.battery_capacity)"
          :grid-voltage="num(latestData?.grid_voltage)"
          :work-mode="numOrNull(latestData?.work_mode)"
          :apparent-power="num(latestData?.output_apparent_power)"
        />
      </el-card>

      <!-- Status & alarms -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header>
          <div style="display: flex; justify-content: space-between; align-items: center;">
            <span>状态与告警</span>
            <el-tag v-if="hasAlarms" type="danger" size="small">{{ alarmCount }}条告警</el-tag>
          </div>
        </template>
        <InverterStatusGrid :latest-data="latestData" />
      </el-card>

      <!-- MPPT details -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header><span>MPPT通道</span></template>
        <InverterMpptCard :data="latestData" />
      </el-card>

      <!-- Temperature & fan -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header><span>温度与风扇</span></template>
        <InverterTempCard :latest-data="latestData" />
      </el-card>

      <!-- Energy generation -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header><span>发电量统计</span></template>
        <InverterEnergyCard :latest-data="latestData" />
      </el-card>

      <!-- Realtime data stream -->
      <el-card style="margin-top: 20px;" shadow="hover">
        <template #header>
          <div style="display: flex; justify-content: space-between; align-items: center;">
            <span>实时数据流</span>
            <el-tag v-if="device?.status === 'online' || device?.status === 'active'" type="success" size="small">实时</el-tag>
          </div>
        </template>
        <RealtimeDataList
          :items="realtimeDataItems"
          :auto-scroll="true"
          :device-type="device?.device_type"
          @clear="clearRealtimeData"
        />
      </el-card>

      <!-- History chart -->
      <HistoryChartSection
        ref="historyChartRef"
        :device-id="deviceId"
        :device-type="device.device_type"
        :device-type-text="deviceTypeText"
        :online="device ? ((device.status === 'online' || device.status === 'active') ? 'online' : 'offline') : 'unknown'"
      />

      <!-- Command frequency -->
      <CommandFrequencySection
        :device-id="deviceId"
        :node-id="deviceNodeId"
        :max-polling-commands="device?.max_polling_commands"
      />

      <!-- Operations -->
      <DeviceControlPanel :device-id="deviceId" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Sunrise, Lightning, Connection, HomeFilled } from '@element-plus/icons-vue'
import DeviceHeader from '../shared/DeviceHeader.vue'
import DeviceInfoCard from '../shared/DeviceInfoCard.vue'
import HistoryChartSection from '../shared/HistoryChartSection.vue'
import CommandFrequencySection from '../shared/CommandFrequencySection.vue'
import DeviceControlPanel from '../shared/DeviceControlPanel.vue'
import InverterPowerFlow from './InverterPowerFlow.vue'
import InverterMpptCard from './InverterMpptCard.vue'
import InverterStatusGrid from './InverterStatusGrid.vue'
import { RUNNING_FLAG_KEYS } from './runningFlags'
import InverterTempCard from './InverterTempCard.vue'
import InverterEnergyCard from './InverterEnergyCard.vue'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
import MetricStatCard from '@/components/common/MetricStatCard.vue'
import { useDeviceData } from '@/composables/useDeviceData'
import { getDeviceTypeLabel } from '@/utils/deviceType'
import { formatPower } from '@/utils/format'

const router = useRouter()
const route = useRoute()
const deviceId = ref(Number(route.params.id) || 0)

const historyChartRef = ref<InstanceType<typeof HistoryChartSection> | null>(null)

const {
  device, loading, refreshing, syncingHA, wsConnected,
  latestData, realtimeDataItems, clearRealtimeData,
  fetchDeviceDetail, fetchLatestData,
  handleRefresh: composableHandleRefresh, handleSyncToHA,
} = useDeviceData(deviceId, { maxItems: 100, errorPrefix: '获取逆变器详情失败' })

const handleRefresh = () => composableHandleRefresh(() => historyChartRef.value?.fetchHistoryData())

const deviceTypeText = computed(() => device.value ? getDeviceTypeLabel(device.value.device_type) : '逆变器')
/**
 * 所属节点数值 id —— 透传给指令频率面板，用于保存后确认节点是否真的应用了新配置
 * （2026-10-15 报障：改前提示永久停在「正在同步到节点」，因为根本没有确认信号）。
 * 优先 device.node.id（后端 Node 关联），回退 node_id 序列号。
 */
const deviceNodeId = computed(() => device.value?.node?.id ?? device.value?.node_id)

// Calculate total PV power from multiple channels
const totalPvPower = computed(() => {
  if (!latestData.value) return 0
  let total = 0
  for (let i = 1; i <= 4; i++) {
    const p = latestData.value[`pv${i}_power`]
    if (typeof p === 'number') total += p
  }
  if (total === 0) {
    total = latestData.value.pv_power ?? latestData.value.solar_power ?? 0
  }
  return total
})

/**
 * 告警计数 —— 只数**真告警位**。
 *
 * ⚠ 不能再用 `key.startsWith('alarm_')` 扫全部字段：
 *   协议里 `alarm_output` / `alarm_pv_to_load` 是**运行状态位**（有输出 / PV 馈能在工作），
 *   而设备正常带载时它们恒为 1。2026-10-20 生产实测：work_mode=市电、fault_code=0，
 *   页面却因为 `alarm_output=1` 常驻「1 条告警」—— 假告警会让真告警失去可信度。
 *   状态位集合由 ./runningFlags.ts 单一真源提供，这里复用而不另写一份前缀判断。
 */
const alarmCount = computed(() => {
  if (!latestData.value) return 0
  let count = 0
  for (const [key, val] of Object.entries(latestData.value)) {
    if (RUNNING_FLAG_KEYS.has(key)) continue
    if (key.startsWith('alarm_') && (val === 1 || (val as any) > 0)) {
      count++
    }
  }
  const faultCode = latestData.value.fault_code ?? latestData.value.error_code
  if (faultCode && faultCode > 0) {
    count++
  }
  return count
})

const hasAlarms = computed(() => alarmCount.value > 0)

function formatNum(v: number | undefined | null, digits: number = 1): string {
  if (v === undefined || v === null || isNaN(v)) return '—'
  return v.toFixed(digits)
}

/**
 * 把可能是字符串的传感器值收敛成 number，缺失时给 0。
 *
 * 为什么需要：latestData 的值有两个来源 —— /latest-values 给 number，
 * 而单条记录解析时 StringValue 优先（见 useDeviceData），字符串型读数
 * （如 BMS hardware_version="V19"）会以 string 形式落进来。直接参与
 * 算术会得到 "1231" 这种字符串拼接结果。
 */
function num(v: unknown): number {
  if (v === undefined || v === null || v === '') return 0
  const n = Number(v)
  return isNaN(n) ? 0 : n
}

/** 同 num，但"缺失"保持为 null —— 用于需要区分"0"与"没有"的展示（如工作模式）。 */
function numOrNull(v: unknown): number | null {
  if (v === undefined || v === null || v === '') return null
  const n = Number(v)
  return isNaN(n) ? null : n
}

/**
 * 带物理闸门的取值：超出 [min, max] 视为**无效**，返回 null。
 *
 * 为什么前端也要挡一道（后端已经修了解析器根因）：
 *   2026-10-19 实测，后端把 HBMS1 的 16 位二进制串当成电池电压，往库里
 *   写了 2087 条 1e12 伏特。根因修好后**历史数据仍在库**，而前端读的是
 *   "每个传感器的最新值" —— 只要最新一条是脏的，页面就显示 1000000000000.0V。
 *
 *   UI 是最后一道防线：显示"—"远好于显示一个荒谬的数字，后者会让人去查
 *   电池，而真相是上游认错了帧。
 */
function numSane(v: unknown, min: number, max: number): number | null {
  if (v === undefined || v === null || v === '') return null
  const n = Number(v)
  if (isNaN(n) || n < min || n > max) return null
  return n
}

/**
 * 电池方向。**充电与放电是两个独立寄存器**，不是同一量的正负号：
 * 泰琪丰协议里 battery_charge_current 与 battery_discharge_current 各自
 * 独立，浮充/待机时两者都可为 0。早期代码读不存在的 battery_current，
 * 恒走 idle 分支，于是语义色永远不亮。
 */
const batteryDirection = computed<'charge' | 'discharge' | 'idle'>(() => {
  if (num(latestData.value?.battery_charge_current) > 0) return 'charge'
  if (num(latestData.value?.battery_discharge_current) > 0) return 'discharge'
  return 'idle'
})

/** 电池电压展示值，经物理闸门（0~1000V）；越界显示 "—" 而非原始数字。 */
const batteryVoltageText = computed(() => {
  const v = numSane(latestData.value?.battery_voltage, 0, 1000)
  return v === null ? '—' : v.toFixed(1)
})

const batteryCurrentText = computed(() => {
  const c = num(latestData.value?.battery_charge_current)
  const d = num(latestData.value?.battery_discharge_current)
  if (c > 0) return `充 ${c.toFixed(1)}A`
  if (d > 0) return `放 ${d.toFixed(1)}A`
  return '静止'
})

const loadSubText = computed(() => {
  const apparent = num(latestData.value?.output_apparent_power)
  const pct = num(latestData.value?.output_load_percent)
  const parts: string[] = []
  if (apparent > 0) parts.push(`${apparent.toFixed(0)}VA`)
  if (pct > 0) parts.push(`${pct.toFixed(0)}%`)
  return parts.join(' · ')
})

const goBack = () => router.back()

onMounted(() => {
  fetchDeviceDetail()
  fetchLatestData()
})
</script>

<style scoped>
.inverter-detail { padding: 0; }

.alarm-list { display: flex; flex-wrap: wrap; gap: 4px; }

/*
  指标卡等高（2026-10-20 二次复审 N2）：
  本页用 el-row/el-col 布局，el-col 默认 align-items:stretch ⇒ 列会被拉成同高，
  但 MetricStatCard 是 el-col 的**直接子元素**，不设 height:100% 时它按自身内容
  高度显示 ⇒ 实测 1024px 下电池卡（副文本折 5 行）127px、其余 110/181px，
  同排底边差最大 71px。

  ⚠ 不要改回固定辅助槽高度来"对齐" —— 那会把多行副文本裁掉（已被复审证伪）。
  正确做法是让卡片撑满列高，内容在卡内居中；高度由最高那张决定，信息完整。
*/
.inverter-detail :deep(.el-row > .el-col > .metric-card) {
  height: 100%;
}

@media (max-width: 768px) {
  /* 实时数据流：桌面列表 569px（约 10 条铺开），窄屏封顶 340px 内部滚动。
     实时流是「看最新几条」的组件，不该在页面里铺满一屏；
     与受控操作的历史容器（max-height 320）采用同一处理方式。 */
  .inverter-detail :deep(.realtime-data-list .plain-list) {
    max-height: 340px;
    overflow-y: auto;
  }

  /* 指标卡同排等高：PV/电网无副文本时 83px，电池/负载有副文本 101px —— 同行错位 18px。
     窄屏统一 min-height，缺副文本的卡由自身居中补齐（不隐藏任何信息）。 */
  .inverter-detail :deep(.el-row > .el-col > .metric-card) {
    min-height: 96px;
  }
  .inverter-detail :deep(.el-row) {
    row-gap: 8px;
  }
}
</style>
