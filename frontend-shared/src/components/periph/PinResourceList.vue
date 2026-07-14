<!--
  PinResourceList.vue — 行式资源控制面板核心组件
  将硬件 GPIO 能力清单与 GPIO/PWM 用户配置合并为唯一行列表。
  每个物理 pin 只出现一次，按 pin 号升序排列。
  规格: docs/设计/GPIO_PWM_UI重设计规格.md
-->
<template>
  <div class="pin-resource-panel">
    <!-- 离线告警 -->
    <el-alert
      v-if="offline"
      type="warning"
      :closable="false"
      title="节点离线，显示最后已知配置"
      show-icon
      class="pin-offline-alert"
    />

    <!-- 加载失败告警 -->
    <el-alert
      v-if="loadError"
      type="error"
      :closable="false"
      title="资源数据加载失败"
      show-icon
      class="pin-error-alert"
    >
      <template #default>
        <span>资源数据加载失败 — <el-button text type="primary" size="small" @click="$emit('retry')">重试</el-button></span>
      </template>
    </el-alert>

    <!-- 首次加载骨架 -->
    <template v-if="initialLoading">
      <el-skeleton :rows="6" animated />
    </template>

    <!-- 空状态 -->
    <el-empty
      v-else-if="pinRows.length === 0"
      description="该节点未报告 GPIO 资源"
      :image-size="60"
    />

    <!-- 列表 -->
    <template v-else>
      <!-- 工具条 -->
      <div class="pin-toolbar">
        <el-radio-group v-model="filterMode" size="small" class="pin-filter">
          <el-radio-button value="all">全部</el-radio-button>
          <el-radio-button value="configured">已配置</el-radio-button>
          <el-radio-button value="available">可用</el-radio-button>
          <el-radio-button value="occupied">已占用</el-radio-button>
        </el-radio-group>
        <el-input
          v-model="searchQuery"
          size="small"
          clearable
          placeholder="搜索引脚号/标签"
          class="pin-search"
        />
        <el-button size="small" @click="$emit('refresh')" :loading="refreshing" class="pin-refresh-btn">
          刷新状态
        </el-button>
      </div>

      <!-- 行列表 -->
      <ul class="pin-resource-list" aria-label="GPIO 与 PWM 引脚资源">
        <li
          v-for="row in filteredRows"
          :key="row.pin"
          class="pin-resource-row"
          :data-state="row.state"
          :aria-busy="row.busy"
        >
          <!-- 桌面四区布局 / 窄屏双层重排 -->
          <div class="pin-identity">
            <span class="pin-number">GPIO {{ row.pin }}</span>
            <span v-if="row.label" class="pin-label">{{ row.label }}</span>
            <el-tag size="small" :type="rowTypeTagType(row.type)" effect="plain">{{ rowTypeLabel(row.type) }}</el-tag>
          </div>

          <div class="pin-configuration">
            <!-- GPIO 已配置 -->
            <template v-if="row.type === 'gpio'">
              <span class="pin-config-text">{{ gpioDirectionLabel(row.gpioConfig!.direction) }}</span>
              <span v-if="row.gpioConfig!.direction === 1" class="pin-config-text">初始 {{ row.gpioConfig!.initial_level === 1 ? 'HIGH' : 'LOW' }}</span>
            </template>
            <!-- PWM 已配置 -->
            <template v-else-if="row.type === 'pwm'">
              <span class="pin-config-text">{{ row.pwmConfig!.frequency }} Hz</span>
              <span class="pin-config-text">{{ row.pwmConfig!.resolution }} bit</span>
              <span v-if="row.pwmConfig!.auto_start" class="pin-config-text">自动启动</span>
            </template>
            <!-- 可用 -->
            <template v-else-if="row.type === 'available'">
              <span class="pin-config-text pin-config-muted">可用</span>
            </template>
            <!-- 已占用 -->
            <template v-else-if="row.type === 'occupied'">
              <el-tag size="small" type="warning" effect="plain">{{ row.occupiedBy }}</el-tag>
            </template>
          </div>

          <div class="pin-runtime">
            <!-- GPIO OUTPUT: el-switch HIGH/LOW -->
            <template v-if="row.type === 'gpio' && row.gpioConfig!.direction === 1">
              <span class="pin-level-indicator">
                <span class="level-dot" :class="levelDotClass(row.gpioLevel)"></span>
                <span class="level-text">{{ levelText(row.gpioLevel) }}</span>
              </span>
              <el-switch
                :model-value="row.gpioLevel === 1"
                :loading="row.busy"
                :disabled="offline || row.busy"
                active-text="HIGH"
                inactive-text="LOW"
                :aria-label="`GPIO ${row.pin} 输出电平`"
                @change="(val: boolean) => handleGpioSet(row, val ? 1 : 0)"
              />
            </template>
            <!-- GPIO INPUT: 读取按钮 -->
            <template v-else-if="row.type === 'gpio' && row.gpioConfig!.direction !== 1">
              <span class="pin-level-indicator">
                <span class="level-dot" :class="levelDotClass(row.gpioLevel)"></span>
                <span class="level-text">{{ levelText(row.gpioLevel) }}</span>
              </span>
              <el-button
                size="small"
                type="primary"
                :loading="row.busy"
                :disabled="offline || row.busy"
                :aria-label="`读取 GPIO ${row.pin} 电平`"
                @click="handleGpioRead(row)"
              >
                读取
              </el-button>
            </template>
            <!-- PWM: 状态 + 占空比 + slider -->
            <template v-else-if="row.type === 'pwm'">
              <el-tag size="small" :type="row.pwmRunning === true ? 'success' : row.pwmRunning === false ? 'info' : 'info'" effect="plain">
                {{ row.pwmRunning === true ? '运行中' : row.pwmRunning === false ? '已停止' : '未知' }}
              </el-tag>
              <span class="pwm-duty-value">{{ (row.pwmDuty / 100).toFixed(2) }}%</span>
              <el-slider
                :model-value="row.pwmDuty"
                :min="0"
                :max="10000"
                :step="10"
                :show-tooltip="false"
                :disabled="offline || row.pwmRunning !== true || row.busy"
                :aria-label="`GPIO ${row.pin} PWM 占空比`"
                class="pin-duty-slider"
                @input="(val: number) => handlePwmDutyInput(row, val)"
                @change="(val: number) => handlePwmDutyChange(row, val)"
              />
            </template>
            <!-- 可用行：无运行时数据 -->
            <template v-else-if="row.type === 'available'">
              <span class="pin-runtime-placeholder"></span>
            </template>
            <!-- 已占用：无运行时数据 -->
            <template v-else>
              <span class="pin-runtime-placeholder"></span>
            </template>
          </div>

          <div class="pin-actions">
            <!-- 可用行：配置 GPIO / 启用 PWM -->
            <template v-if="row.type === 'available'">
              <el-button
                size="small"
                type="primary"
                :disabled="offline"
                @click="$emit('configure-gpio', row.pin)"
              >
                配置 GPIO
              </el-button>
              <el-button
                size="small"
                :disabled="offline"
                @click="$emit('configure-pwm', row.pin)"
              >
                启用 PWM
              </el-button>
            </template>

            <!-- GPIO 已配置 -->
            <template v-else-if="row.type === 'gpio'">
              <el-button size="small" text :disabled="offline" @click="$emit('edit-gpio', row.pin)">编辑</el-button>
              <el-dropdown trigger="click" @command="(cmd: string) => handleDropdownCommand(row, cmd)">
                <el-button size="small" text :disabled="offline">更多 ⏷</el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="remove" class="pin-danger-item">移除配置</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </template>

            <!-- PWM 已配置 -->
            <template v-else-if="row.type === 'pwm'">
              <el-button
                v-if="row.pwmRunning !== true"
                size="small"
                type="primary"
                :loading="row.busy"
                :disabled="offline || row.busy"
                @click="handlePwmStart(row)"
              >
                启动
              </el-button>
              <el-button
                v-else
                size="small"
                :loading="row.busy"
                :disabled="offline || row.busy"
                @click="handlePwmStop(row)"
              >
                停止
              </el-button>
              <el-button size="small" text :disabled="offline" @click="$emit('edit-pwm', row.pin)">编辑</el-button>
              <el-dropdown trigger="click" @command="(cmd: string) => handleDropdownCommand(row, cmd)">
                <el-button size="small" text :disabled="offline">更多 ⏷</el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item command="remove" class="pin-danger-item">移除配置</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </template>

            <!-- 已占用：查看占用 -->
            <template v-else-if="row.type === 'occupied'">
              <el-button size="small" text @click="$emit('view-occupied', row.occupiedBy)">查看占用</el-button>
            </template>
          </div>

          <!-- 行内反馈 -->
          <div v-if="row.feedback" class="pin-feedback" role="status">
            <span :class="row.feedbackType === 'error' ? 'pin-feedback-error' : 'pin-feedback-info'">{{ row.feedback }}</span>
          </div>
        </li>
      </ul>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, reactive } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { gpioApi, pwmApi, type GPIOConfig, type PWMConfig } from '@/api/periph'

// ============================================================
// 类型定义
// ============================================================

export type PinType = 'available' | 'gpio' | 'pwm' | 'occupied'
export type PinState = 'available' | 'gpio' | 'pwm' | 'occupied' | 'offline' | 'error'

export interface PinRow {
  pin: number
  hwId: string
  type: PinType
  state: PinState
  label: string
  busy: boolean
  feedback: string
  feedbackType: 'info' | 'error' | ''
  // GPIO
  gpioConfig?: GPIOConfig
  gpioLevel: number | null  // null = 未知
  // PWM
  pwmConfig?: PWMConfig
  pwmRunning: boolean | null  // null = 未知
  pwmDuty: number
  // occupied
  occupiedBy?: string
}

interface Props {
  /** 硬件 GPIO 能力清单 */
  hardwareGpio: any[]
  /** 已配置 GPIO 列表 */
  gpioConfigs: GPIOConfig[]
  /** 已配置 PWM 列表 */
  pwmConfigs: PWMConfig[]
  /** 节点 ID */
  nodeId: string
  /** 离线 */
  offline?: boolean
  /** 初始加载中 */
  initialLoading?: boolean
  /** 刷新中 */
  refreshing?: boolean
  /** 加载失败 */
  loadError?: boolean
  /** 已占用引脚映射: pin → 占用描述 (如 "UART TX") */
  occupiedPins?: Map<number, string>
}

const props = withDefaults(defineProps<Props>(), {
  offline: false,
  initialLoading: false,
  refreshing: false,
  loadError: false,
  occupiedPins: () => new Map(),
})

const emit = defineEmits<{
  (e: 'configure-gpio', pin: number): void
  (e: 'configure-pwm', pin: number): void
  (e: 'edit-gpio', pin: number): void
  (e: 'edit-pwm', pin: number): void
  (e: 'remove-gpio', pin: number): void
  (e: 'remove-pwm', pin: number): void
  (e: 'view-occupied', occupiedBy: string): void
  (e: 'refresh'): void
  (e: 'retry'): void
  (e: 'row-updated'): void
}>()

// ============================================================
// 行数据归一化
// ============================================================

/** 从硬件能力清单、GPIO/PWM 配置、占用映射合并出唯一行列表 */
const pinRows = computed<PinRow[]>(() => {
  const rows: PinRow[] = []
  const pinMap = new Map<number, PinRow>()

  // 1. 以硬件能力清单为基础生成行
  for (const hw of props.hardwareGpio) {
    const pin = hw.pin ?? (typeof hw.id === 'string' ? parseInt(hw.id.replace(/\D/g, '')) : 0)
    if (pinMap.has(pin)) continue

    const gpioCfg = props.gpioConfigs.find(g => g.pin === pin)
    const pwmCfg = props.pwmConfigs.find(p => p.pin === pin)
    const occupiedBy = props.occupiedPins.get(pin)

    let type: PinType = 'available'
    let state: PinState = 'available'
    let label = ''
    let gpioLevel: number | null = null
    let pwmRunning: boolean | null = null
    let pwmDuty = 0

    if (gpioCfg) {
      type = 'gpio'
      state = 'gpio'
      label = gpioCfg.label || ''
      gpioLevel = gpioCfg.direction === 1 ? gpioCfg.initial_level : null
    } else if (pwmCfg) {
      type = 'pwm'
      state = 'pwm'
      label = pwmCfg.label || ''
      pwmDuty = pwmCfg.duty
      pwmRunning = null  // 未知，不伪造
    } else if (occupiedBy) {
      type = 'occupied'
      state = 'occupied'
    }

    const row: PinRow = reactive({
      pin,
      hwId: hw.id || `GPIO${pin}`,
      type,
      state,
      label,
      busy: false,
      feedback: '',
      feedbackType: '' as const,
      gpioConfig: gpioCfg,
      gpioLevel,
      pwmConfig: pwmCfg,
      pwmRunning,
      pwmDuty,
      occupiedBy,
    })

    pinMap.set(pin, row)
    rows.push(row)
  }

  // 2. 补充: GPIO/PWM 配置中可能有不在硬件清单里的 pin（降级处理）
  for (const gc of props.gpioConfigs) {
    if (!pinMap.has(gc.pin)) {
      const row: PinRow = reactive({
        pin: gc.pin,
        hwId: `GPIO${gc.pin}`,
        type: 'gpio',
        state: 'gpio',
        label: gc.label || '',
        busy: false,
        feedback: '',
        feedbackType: '' as const,
        gpioConfig: gc,
        gpioLevel: gc.direction === 1 ? gc.initial_level : null,
      })
      pinMap.set(gc.pin, row)
      rows.push(row)
    }
  }
  for (const pc of props.pwmConfigs) {
    if (!pinMap.has(pc.pin)) {
      const row: PinRow = reactive({
        pin: pc.pin,
        hwId: `GPIO${pc.pin}`,
        type: 'pwm',
        state: 'pwm',
        label: pc.label || '',
        busy: false,
        feedback: '',
        feedbackType: '' as const,
        pwmConfig: pc,
        pwmRunning: null,
        pwmDuty: pc.duty,
      })
      pinMap.set(pc.pin, row)
      rows.push(row)
    }
  }

  // 3. 按引脚号升序
  rows.sort((a, b) => a.pin - b.pin)
  return rows
})

// ============================================================
// 筛选与搜索
// ============================================================

const filterMode = ref<'all' | 'configured' | 'available' | 'occupied'>('all')
const searchQuery = ref('')

const filteredRows = computed(() => {
  let result = pinRows.value

  // 状态筛选
  switch (filterMode.value) {
    case 'configured':
      result = result.filter(r => r.type === 'gpio' || r.type === 'pwm')
      break
    case 'available':
      result = result.filter(r => r.type === 'available')
      break
    case 'occupied':
      result = result.filter(r => r.type === 'occupied')
      break
  }

  // 搜索
  const q = searchQuery.value.trim().toLowerCase()
  if (q) {
    result = result.filter(r =>
      String(r.pin).includes(q) ||
      r.label.toLowerCase().includes(q) ||
      r.hwId.toLowerCase().includes(q)
    )
  }

  return result
})

// ============================================================
// 辅助函数
// ============================================================

function rowTypeTagType(type: PinType): string {
  switch (type) {
    case 'gpio': return 'primary'
    case 'pwm': return 'success'
    case 'available': return 'info'
    case 'occupied': return 'warning'
    default: return 'info'
  }
}

function rowTypeLabel(type: PinType): string {
  switch (type) {
    case 'gpio': return 'GPIO'
    case 'pwm': return 'PWM'
    case 'available': return '可用'
    case 'occupied': return '已占用'
    default: return ''
  }
}

function gpioDirectionLabel(direction: number): string {
  const labels = ['INPUT', 'OUTPUT', 'INPUT_PULLUP', 'INPUT_PULLDOWN']
  return labels[direction] || 'UNKNOWN'
}

function levelText(level: number | null): string {
  if (level === 1) return 'HIGH'
  if (level === 0) return 'LOW'
  return '未知'
}

function levelDotClass(level: number | null): string {
  if (level === 1) return 'level-high'
  if (level === 0) return 'level-low'
  return 'level-unknown'
}

// ============================================================
// 行操作 — GPIO
// ============================================================

async function handleGpioSet(row: PinRow, level: 0 | 1) {
  if (row.busy || props.offline) return
  row.busy = true
  row.feedback = `正在写入 ${level === 1 ? 'HIGH' : 'LOW'}…`
  row.feedbackType = 'info'
  const prevLevel = row.gpioLevel
  row.gpioLevel = level
  try {
    await gpioApi.set(props.nodeId, row.pin, level)
    row.feedback = ''
    row.feedbackType = ''
    ElMessage.success(`GPIO ${row.pin} ${level === 1 ? 'HIGH' : 'LOW'}`)
  } catch (e: any) {
    row.gpioLevel = prevLevel
    row.feedback = `写入失败 · 重试`
    row.feedbackType = 'error'
    ElMessage.error(`GPIO 操作失败: ${e?.message || '未知错误'}`)
  } finally {
    row.busy = false
  }
}

async function handleGpioRead(row: PinRow) {
  if (row.busy || props.offline) return
  row.busy = true
  row.feedback = '正在读取…'
  row.feedbackType = 'info'
  try {
    const result = await gpioApi.read(props.nodeId, row.pin)
    row.gpioLevel = result.level
    row.feedback = ''
    row.feedbackType = ''
  } catch (e: any) {
    row.feedback = '读取失败 · 重试'
    row.feedbackType = 'error'
    ElMessage.error(`GPIO 读取失败: ${e?.message || '未知错误'}`)
  } finally {
    row.busy = false
  }
}

// ============================================================
// 行操作 — PWM
// ============================================================

let dutyTimer: ReturnType<typeof setTimeout> | null = null

function handlePwmDutyInput(row: PinRow, val: number) {
  row.pwmDuty = val
}

async function handlePwmDutyChange(row: PinRow, val: number) {
  if (dutyTimer) clearTimeout(dutyTimer)
  row.busy = true
  row.feedback = '待应用'
  row.feedbackType = 'info'

  dutyTimer = setTimeout(async () => {
    try {
      await pwmApi.setDuty(props.nodeId, row.pin, val)
      row.feedback = ''
      row.feedbackType = ''
    } catch (e: any) {
      // 回滚到服务端确认值
      try {
        const state = await pwmApi.getState(props.nodeId, row.pin)
        row.pwmDuty = state.duty
      } catch {
        // 保持原值但标记错误
      }
      row.feedback = '占空比设置失败 · 重试'
      row.feedbackType = 'error'
      ElMessage.error(`PWM 占空比设置失败: ${e?.message || '未知错误'}`)
    } finally {
      row.busy = false
    }
  }, 300)
}

async function handlePwmStart(row: PinRow) {
  if (row.busy || props.offline) return
  row.busy = true
  row.feedback = '正在启动…'
  row.feedbackType = 'info'
  try {
    await pwmApi.start(props.nodeId, row.pin)
    row.pwmRunning = true
    row.feedback = ''
    row.feedbackType = ''
    ElMessage.success(`PWM GPIO${row.pin} 已启动`)
  } catch (e: any) {
    row.feedback = '启动失败 · 重试'
    row.feedbackType = 'error'
    ElMessage.error(`PWM 启动失败: ${e?.message || '未知错误'}`)
  } finally {
    row.busy = false
  }
}

async function handlePwmStop(row: PinRow) {
  if (row.busy || props.offline) return
  row.busy = true
  row.feedback = '正在停止…'
  row.feedbackType = 'info'
  try {
    await pwmApi.stop(props.nodeId, row.pin)
    row.pwmRunning = false
    row.feedback = ''
    row.feedbackType = ''
    ElMessage.success(`PWM GPIO${row.pin} 已停止`)
  } catch (e: any) {
    row.feedback = '停止失败 · 重试'
    row.feedbackType = 'error'
    ElMessage.error(`PWM 停止失败: ${e?.message || '未知错误'}`)
  } finally {
    row.busy = false
  }
}

// ============================================================
// 更多菜单 — 移除配置（危险操作，二次确认）
// ============================================================

function handleDropdownCommand(row: PinRow, command: string) {
  if (command === 'remove') {
    handleRemove(row)
  }
}

async function handleRemove(row: PinRow) {
  const pinLabel = `GPIO ${row.pin}`
  const currentUse = row.type === 'gpio' ? 'GPIO 配置' : row.type === 'pwm' ? 'PWM 配置' : '配置'
  try {
    await ElMessageBox.confirm(
      `确定移除 ${pinLabel} 的${currentUse}？该操作会停止当前输出并释放引脚。`,
      '移除确认',
      { type: 'warning', confirmButtonText: '移除', cancelButtonText: '取消' }
    )
  } catch {
    return  // 用户取消
  }

  row.busy = true
  row.feedback = '正在移除…'
  row.feedbackType = 'info'
  try {
    if (row.type === 'gpio') {
      await gpioApi.delete(props.nodeId, row.pin)
      emit('remove-gpio', row.pin)
    } else if (row.type === 'pwm') {
      await pwmApi.delete(props.nodeId, row.pin)
      emit('remove-pwm', row.pin)
    }
    row.feedback = ''
    row.feedbackType = ''
    ElMessage.success(`${pinLabel} 配置已移除`)
    emit('row-updated')
  } catch (e: any) {
    row.feedback = '移除失败 · 重试'
    row.feedbackType = 'error'
    ElMessage.error(`移除失败: ${e?.message || '未知错误'}`)
  } finally {
    row.busy = false
  }
}

// ============================================================
// 暴露: 允许父组件刷新行内运行态
// ============================================================

defineExpose({
  pinRows,
  refreshGpioLevel: async (pin: number) => {
    const row = pinRows.value.find(r => r.pin === pin && r.type === 'gpio')
    if (!row || row.gpioConfig?.direction !== 0) return
    try {
      const result = await gpioApi.read(props.nodeId, pin)
      row.gpioLevel = result.level
    } catch {
      // 静默
    }
  },
  refreshPwmState: async (pin: number) => {
    const row = pinRows.value.find(r => r.pin === pin && r.type === 'pwm')
    if (!row) return
    try {
      const state = await pwmApi.getState(props.nodeId, pin)
      row.pwmRunning = state.running
      row.pwmDuty = state.duty
    } catch {
      // 静默 — 保持未知
    }
  },
})
</script>

<style scoped>
.pin-resource-panel {
  /* container query context: allows descendants to respond to *actual*
   * available width, not viewport width which is wrong when a sidebar
   * consumes space. */
  container-type: inline-size;
  container-name: pin-panel;
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}

.pin-offline-alert,
.pin-error-alert {
  margin-bottom: 4px;
}

/* ---- 工具条 ---- */
.pin-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  position: sticky;
  top: 0;
  z-index: 1;
  background: var(--el-bg-color);
  padding: 4px 0;
}

.pin-filter {
  flex-shrink: 0;
  min-width: 0;
}

.pin-search {
  width: 220px;
  max-width: 100%;
  flex-shrink: 1;
}

.pin-refresh-btn {
  flex-shrink: 0;
  margin-left: auto;
}

/* ---- 行列表 ---- */
.pin-resource-list {
  list-style: none;
  margin: 0;
  padding: 0;
  min-width: 0;
}

.pin-resource-row {
  display: grid;
  grid-template-columns: minmax(0, 1.1fr) minmax(0, 1.2fr) minmax(0, 2fr) auto;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  min-height: 64px;
  min-width: 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
  transition: background-color 0.2s;
  position: relative;
}

.pin-resource-row:hover {
  background: var(--el-fill-color-light);
}

/* 左色条: GPIO primary, PWM success */
.pin-resource-row[data-state="gpio"]::before {
  content: '';
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 3px;
  background: var(--el-color-primary);
}
.pin-resource-row[data-state="pwm"]::before {
  content: '';
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 3px;
  background: var(--el-color-success);
}

/* 可用行紧凑 */
.pin-resource-row[data-state="available"] {
  min-height: 48px;
}

/* ---- 各区域 ---- */
.pin-identity {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}

.pin-number {
  font-weight: 600;
  font-size: 14px;
  font-family: 'JetBrains Mono', 'Cascadia Code', 'Courier New', monospace;
  white-space: nowrap;
}

.pin-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 120px;
}

.pin-configuration {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}

.pin-config-text {
  font-size: 13px;
  color: var(--el-text-color-regular);
  white-space: nowrap;
}

.pin-config-muted {
  color: var(--el-text-color-placeholder);
}

.pin-runtime {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  min-width: 0;
}

.pin-runtime-placeholder {
  width: 100%;
}

.pin-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  justify-content: flex-end;
  flex-shrink: 0;
  flex-wrap: wrap;
  min-width: 0;
}

/* ---- 电平指示器 ---- */
.pin-level-indicator {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.level-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--el-text-color-disabled);
  flex-shrink: 0;
}

.level-dot.level-high {
  background: var(--el-color-success);
}

.level-dot.level-low {
  background: var(--el-text-color-secondary);
}

.level-dot.level-unknown {
  background: var(--el-text-color-placeholder);
}

.level-text {
  font-size: 13px;
  font-family: monospace;
  white-space: nowrap;
}

/* ---- PWM 占空比 ---- */
.pwm-duty-value {
  font-family: monospace;
  font-weight: 600;
  font-size: 13px;
  white-space: nowrap;
}

.pin-duty-slider {
  width: 100%;
  min-width: 80px;
  max-width: 180px;
  flex-shrink: 1;
}

/* ---- 反馈 ---- */
.pin-feedback {
  grid-column: 1 / -1;
  padding-top: 4px;
  font-size: 12px;
}

.pin-feedback-info {
  color: var(--el-text-color-secondary);
}

.pin-feedback-error {
  color: var(--el-color-danger);
}

/* ---- 危险菜单项 ---- */
.pin-danger-item {
  color: var(--el-color-danger);
}

/* ---- 容器查询响应式: 当组件可用宽度 ≤600px 时采用双层重排 ----
 * 不再依赖 viewport @media 作为唯一触发条件，因为侧边栏后的实际容器
 * 宽度可能远小于 viewport 宽度。 */
@container pin-panel (max-width: 600px) {
  .pin-resource-row {
    grid-template-columns: 1fr auto;
    gap: 8px;
    padding: 10px 12px;
  }

  .pin-configuration,
  .pin-runtime,
  .pin-feedback {
    grid-column: 1 / -1;
  }

  .pin-actions {
    grid-column: 2;
    grid-row: 1;
    flex-direction: column;
    align-items: flex-end;
    gap: 4px;
  }

  .pin-identity {
    grid-column: 1;
  }

  .pin-duty-slider {
    max-width: 100%;
    width: 100%;
  }

  .pin-toolbar {
    flex-wrap: wrap;
  }

  .pin-filter {
    width: 100%;
    overflow-x: auto;
  }

  .pin-search {
    width: 100%;
    flex: 1 1 120px;
  }

  .pin-refresh-btn {
    margin-left: 0;
  }
}

/* ---- 保留 viewport @media 作为渐进增强 (辅助，非唯一触发) ---- */
@media (max-width: 768px) {
  .pin-toolbar {
    flex-wrap: wrap;
  }

  .pin-filter {
    width: 100%;
    overflow-x: auto;
  }

  .pin-search {
    width: 100%;
    flex: 1 1 120px;
  }

  .pin-refresh-btn {
    margin-left: 0;
  }
}
</style>
