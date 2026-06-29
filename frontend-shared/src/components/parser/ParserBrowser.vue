<template>
  <el-dialog
    v-model="visible"
    title="选择解析器"
    width="720px"
    :close-on-click-modal="true"
    class="parser-browser-dialog"
  >
    <!-- 搜索过滤 -->
    <div class="search-bar">
      <el-input
        v-model="searchKeyword"
        placeholder="搜索解析器..."
        :prefix-icon="Search"
        clearable
        style="width: 300px;"
      />

      <el-select v-model="hardwareTypeFilter" placeholder="硬件类型" clearable style="width: 140px;">
        <el-option label="UART" value="uart" />
        <el-option label="I2C" value="i2c" />
        <el-option label="SPI" value="spi" />
        <el-option label="GPIO" value="gpio" />
        <el-option label="ADC" value="adc" />
      </el-select>
    </div>

    <!-- 解析器列表（按厂商分组） -->
    <div class="parser-list" v-loading="loading">
      <el-collapse v-model="expandedVendors">
        <el-collapse-item
          v-for="(parsers, vendor) in filteredParsersByVendor"
          :key="vendor"
          :title="vendor"
          :name="vendor"
        >
          <template #title>
            <div class="vendor-header">
              <span class="vendor-name">{{ vendor }}</span>
              <el-tag size="small">{{ parsers.length }} 个解析器</el-tag>
            </div>
          </template>

          <div class="parser-grid">
            <div
              v-for="parser in parsers"
              :key="parser.id"
              class="parser-card"
              :class="{ selected: selectedParser?.id === parser.id }"
              @click="selectParser(parser)"
            >
              <div class="parser-icon">
                <el-icon :size="28"><Cpu /></el-icon>
              </div>
              <div class="parser-info">
                <h4>{{ parser.name }}</h4>
                <p class="parser-id">{{ parser.id }}</p>
                <div class="parser-tags">
                  <el-tag
                    v-for="bus in parser.hardware_types"
                    :key="bus"
                    size="small"
                    :type="getBusTagType(bus) as any"
                  >
                    {{ bus.toUpperCase() }}
                  </el-tag>
                  <el-tag
                    v-for="measure in parser.measure_types"
                    :key="measure"
                    size="small"
                    type="info"
                  >
                    {{ getMeasureLabel(measure) }}
                  </el-tag>
                </div>
              </div>
              <div v-if="selectedParser?.id === parser.id" class="selected-badge">
                <el-icon><Check /></el-icon>
              </div>
            </div>
          </div>
        </el-collapse-item>
      </el-collapse>
    </div>

    <!-- 已选解析器预览 -->
    <div v-if="selectedParser" class="selected-preview">
      <el-alert type="success" :closable="false">
        <template #title>
          已选择: <strong>{{ selectedParser.name }}</strong>
          <span class="parser-id-label">{{ selectedParser.id }}</span>
        </template>
        <div style="margin-top: 8px;">
          <el-tag
            v-for="bus in selectedParser.hardware_types"
            :key="bus"
            size="small"
            style="margin-right: 4px;"
          >
            {{ bus.toUpperCase() }}
          </el-tag>
          <span style="margin: 0 8px; color: var(--el-text-color-secondary);">|</span>
          <el-tag
            v-for="measure in selectedParser.measure_types"
            :key="measure"
            size="small"
            type="info"
            style="margin-right: 4px;"
          >
            {{ getMeasureLabel(measure) }}
          </el-tag>
        </div>
      </el-alert>
    </div>

    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :disabled="!selectedParser" @click="confirmSelection">
        确认选择
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { Search, Cpu, Check } from '@element-plus/icons-vue'
import { useParserStore } from '@/stores/parser'
import type { Parser } from '@/api/parser'

export interface ParserBrowserProps {
  modelValue: boolean
  preSelected?: Parser | null
}

const props = withDefaults(defineProps<ParserBrowserProps>(), {
  modelValue: false,
  preSelected: null
})

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  'select': [parser: Parser]
}>()

const parserStore = useParserStore()

const visible = computed({
  get: () => props.modelValue,
  set: (val) => emit('update:modelValue', val)
})

const selectedParser = ref<Parser | null>(props.preSelected || null)
const searchKeyword = ref('')
const hardwareTypeFilter = ref('')
const expandedVendors = ref<string[]>([])

const loading = computed(() => parserStore.loading)

// 过滤后的解析器（按厂商分组）
const filteredParsersByVendor = computed(() => {
  let parsers = parserStore.parsers

  if (searchKeyword.value) {
    const kw = searchKeyword.value.toLowerCase()
    parsers = parsers.filter(p =>
      p.name.toLowerCase().includes(kw) ||
      p.id.toLowerCase().includes(kw) ||
      p.category.toLowerCase().includes(kw)
    )
  }

  if (hardwareTypeFilter.value) {
    parsers = parsers.filter(p => p.hardware_types.includes(hardwareTypeFilter.value))
  }

  const grouped: Record<string, Parser[]> = {}
  for (const p of parsers) {
    if (!grouped[p.vendor]) grouped[p.vendor] = []
    grouped[p.vendor].push(p)
  }

  // 默认展开第一个有结果的厂商
  if (expandedVendors.value.length === 0 && Object.keys(grouped).length > 0) {
    expandedVendors.value = [Object.keys(grouped)[0]]
  }

  return grouped
})

const getBusTagType = (bus: string): string => {
  const types: Record<string, string> = {
    uart: '',
    i2c: 'warning',
    spi: 'danger',
    gpio: 'success',
    adc: 'primary',
    pwm: 'info'
  }
  return types[bus] || 'info'
}

const getMeasureLabel = (measure: string): string => {
  const labels: Record<string, string> = {
    temperature: '温度',
    humidity: '湿度',
    pressure: '气压',
    wind_speed: '风速',
    wind_direction: '风向',
    light: '光照',
    rain: '雨量',
    gas: '气体',
    voltage: '电压',
    current: '电流'
  }
  return labels[measure] || measure
}

const selectParser = (parser: Parser) => {
  selectedParser.value = parser
}

const confirmSelection = () => {
  if (selectedParser.value) {
    emit('select', selectedParser.value)
    visible.value = false
  }
}

watch(() => props.modelValue, (val) => {
  if (val) {
    selectedParser.value = props.preSelected || null
    if (parserStore.parsers.length === 0) {
      parserStore.fetchParsers()
    }
  }
})
</script>

<style scoped>
.search-bar {
  display: flex;
  gap: 12px;
  margin-bottom: 16px;
  padding-bottom: 16px;
  border-bottom: 1px solid #f0f0f0;
}

.parser-list {
  max-height: 400px;
  overflow-y: auto;
}

.vendor-header {
  display: flex;
  align-items: center;
  gap: 8px;
}

.vendor-name {
  font-weight: 500;
  font-size: 15px;
}

.parser-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
  padding: 8px 0;
}

.parser-card {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 12px;
  border: 1px solid #e8eaec;
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.2s;
  position: relative;
}

.parser-card:hover {
  border-color: var(--el-color-primary);
  background: #f0f9eb;
}

.parser-card.selected {
  border-color: var(--el-color-success);
  background: #f0f9eb;
}

.parser-icon {
  width: 44px;
  height: 44px;
  border-radius: 8px;
  background: linear-gradient(135deg, var(--el-color-primary) 0%, var(--el-color-success) 100%);
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  flex-shrink: 0;
}

.parser-info {
  flex: 1;
  min-width: 0;
}

.parser-info h4 {
  margin: 0 0 4px;
  font-size: 14px;
  color: #303133;
}

.parser-id {
  margin: 0 0 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  font-family: monospace;
}

.parser-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.selected-badge {
  position: absolute;
  top: 8px;
  right: 8px;
  width: 20px;
  height: 20px;
  border-radius: 50%;
  background: var(--el-color-success);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
}

.selected-preview {
  margin-top: 16px;
}

.parser-id-label {
  font-family: monospace;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-left: 8px;
}
</style>
