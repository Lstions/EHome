# DMA 开关嵌入硬件资源条目 实现计划

> **Goal:** 将 DMA 开关从独立的 "DMA 通道" 卡片区域移动到 ChannelPanel 中每个硬件资源条目的右侧，与 `+ 创建通道` 同级。UART0/UART1 共享 1 个 DMA 时两个开关互斥。

**Architecture:** 修改 ChannelPanel.vue 的 `hardware-card-header` 区域，在 `hw.enabled` checkbox 旁边增加 DMA 开关。DMA 数据从 NodeDetail 通过 props 传入 ChannelPanel。互斥逻辑在前端通过 computed 实现。

**Tech Stack:** Vue 3 + Element Plus + TypeScript

---

### Task 1: ChannelPanel 新增 dmaChannels prop

**Objective:** 让 ChannelPanel 接收 DMA 通道数据

**Files:**
- Modify: `frontend-shared/src/components/node/ChannelPanel.vue`

**Step 1: 添加 Props 接口**

在 `interface Props` 中新增：
```typescript
interface Props {
  collectorId: number
  collectorStatus?: string
  dmaChannels?: DmaChannelInfo[]  // 新增
}
```

**Step 2: 从 NodeDetail 传入 dmaChannels**

在 `NodeDetail.vue` 的 ChannelPanel 标签中：
```html
<ChannelPanel 
  ref="busConfigPanelRef" 
  :collector-id="collectorId" 
  :collector-status="collector?.status"
  :dma-channels="dmaChannels"
/>
```

---

### Task 2: 实现 DMA 开关 UI（hardware-card-header 右侧）

**Objective:** 在每个硬件资源条目右侧显示 DMA 开关

**Files:**
- Modify: `frontend-shared/src/components/node/ChannelPanel.vue`

**Step 1: 在 hardware-card-header 右侧添加 DMA 开关**

修改 `hardware-card-header` 模板（第 60-68 行），在 `el-checkbox` 后面增加 DMA 开关区域：

```html
<div class="hardware-card-header">
  <div class="hardware-main">
    <span class="hardware-id">{{ hw.name || hw.id }}</span>
    <el-tag :type="getBusTagType(busType)" size="small" effect="plain">{{ busType.toUpperCase() }}</el-tag>
    <PinBadges :pins="hw.pins" />
    <span class="hardware-info">{{ getHardwareInfo(hw) }}</span>
  </div>
  <div class="hardware-actions">
    <!-- DMA 开关（仅对有 DMA 支持的总线类型显示） -->
    <template v-if="supportsDma(busType)">
      <el-switch
        v-for="dma in getDmaForHardware(busType, hw)"
        :key="dma.dma_id"
        :model-value="dma.enabled"
        :disabled="!canToggleDma(dma, busType, hw)"
        @change="(val: boolean) => toggleDma(dma, val)"
        size="small"
        :active-text="dma.name"
        class="dma-switch"
      />
    </template>
    <el-checkbox v-model="hw.enabled" disabled size="small" class="hw-enabled-checkbox" />
  </div>
</div>
```

**Step 2: 添加 computed 辅助函数**

```typescript
// 哪些总线类型支持 DMA
const supportsDma = (busType: string): boolean => {
  return ['uart', 'spi', 'i2c'].includes(busType)
}

// 获取该硬件资源可用的 DMA 通道
const getDmaForHardware = (busType: string, hw: any): DmaChannelInfo[] => {
  if (!props.dmaChannels) return []
  const busMask = busTypeToMask(busType)
  return props.dmaChannels.filter(dma => (dma.compatible_bus & busMask) !== 0)
}

const busTypeToMask = (busType: string): number => {
  switch (busType) {
    case 'uart': return 1
    case 'i2c': return 2
    case 'spi': return 4
    default: return 0
  }
}

// 互斥检查：同类型硬件资源中，如果其他资源已占用该 DMA，则当前不可切换
const canToggleDma = (dma: DmaChannelInfo, busType: string, hw: any): boolean => {
  if (dma.enabled) return true  // 已启用的可以关闭
  // 检查同 busType 的其他硬件资源是否已占用该 DMA
  const siblings = hardware.value[busType] || []
  for (const sibling of siblings) {
    if (sibling.id === hw.id) continue
    // 如果兄弟资源已启用该 DMA，则互斥
    if (sibling._dmaEnabled && sibling._dmaId === dma.dma_id) return false
  }
  return true
}
```

---

### Task 3: DMA 状态管理与互斥逻辑

**Objective:** 在 hardware 数据中维护 DMA 绑定状态，实现互斥

**Files:**
- Modify: `frontend-shared/src/components/node/ChannelPanel.vue`

**Step 1: 在 refreshBuses 中初始化 DMA 状态**

在合并 hardware 数据时（约 340-370 行），为每个资源添加 DMA 字段：
```typescript
mergedHardware[type] = capHardware[type].map((r: any) => ({
  ...r,
  enabled: false,
  config_id: null,
  _dmaEnabled: false,   // 新增
  _dmaId: null as number | null  // 新增
}))
```

**Step 2: 实现 toggleDma**

```typescript
const toggleDma = (dma: DmaChannelInfo, enabled: boolean) => {
  // 找到当前操作的硬件资源
  // 通过遍历 hardware 找到当前正在操作的 hw
  for (const busType of ['i2c', 'uart', 'spi']) {
    const resources = hardware.value[busType] || []
    for (const hw of resources) {
      // 如果该 DMA 被此资源绑定或将要绑定
      if (enabled) {
        // 启用：先禁用同 busType 其他资源上的该 DMA
        for (const sibling of resources) {
          if (sibling._dmaId === dma.dma_id) {
            sibling._dmaEnabled = false
            sibling._dmaId = null
          }
        }
        hw._dmaEnabled = true
        hw._dmaId = dma.dma_id
      } else {
        hw._dmaEnabled = false
        hw._dmaId = null
      }
      return
    }
  }
}
```

---

### Task 4: 样式调整

**Objective:** DMA 开关与 checkbox 并排显示，样式协调

**Files:**
- Modify: `frontend-shared/src/components/node/ChannelPanel.vue`

```css
.hardware-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}

.dma-switch {
  --el-switch-on-color: #409eff;
  --el-switch-off-color: #dcdfe6;
}

.dma-switch .el-switch__label {
  font-size: 11px;
  font-family: 'Courier New', monospace;
}
```

---

### Task 5: 移除 NodeDetail 中独立的 DMA 卡片区域

**Objective:** 清理旧 UI

**Files:**
- Modify: `frontend-shared/src/views/node/NodeDetail.vue`

删除第 130-192 行的 DMA 通道卡片（`<el-card v-if="collector" style="margin-top: 20px;">` 整个块）。
保留 `loadDmaChannels()` 调用（在 onMounted 中），因为数据仍需传给 ChannelPanel。

---

### Task 6: 验证

**Step 1:** 启动前端 `cd frontend-shared && pnpm dev`
**Step 2:** 浏览器访问 `http://localhost:5174/node/2`（C6 节点）
**Step 3:** 确认 I2C0/UART0/UART1/SPI2 右侧显示 DMA 开关（GDMA_CH0/CH1/CH2）
**Step 4:** 测试互斥：启用 UART0 的 GDMA_CH0 → UART1 的 GDMA_CH0 应变为 disabled
**Step 5:** 保存配置后刷新页面，确认 DMA 状态持久化
