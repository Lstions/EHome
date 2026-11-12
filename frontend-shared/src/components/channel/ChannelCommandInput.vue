<template>
  <el-card class="terminal-send-card" shadow="never">
    <div class="terminal-input">
      <el-radio-group
        :model-value="inputMode"
        size="small"
        class="input-mode-toggle"
        @update:model-value="onModeChange"
      >
        <el-radio-button value="hex">HEX</el-radio-button>
        <el-radio-button value="ascii">ASCII</el-radio-button>
      </el-radio-group>

      <el-input
        v-if="inputMode === 'hex'"
        :model-value="inputData"
        placeholder="输入 hex 数据（如 01 03 00 00 00 02 C4 0B）"
        size="small"
        @update:model-value="emit('update:inputData', $event)"
        @keyup.enter="emit('send')"
        @keydown="onKeydown"
        :disabled="disabled || sending"
        clearable
        style="flex: 1;"
        class="hex-input"
      >
        <template #prefix>
          <span class="tx-prefix">TX&gt;</span>
        </template>
      </el-input>

      <el-input
        v-else
        :model-value="inputDataAscii"
        placeholder="输入 ASCII 明文（如 AT+RST）"
        size="small"
        @update:model-value="emit('update:inputDataAscii', $event)"
        @keyup.enter="emit('send')"
        @keydown="onKeydown"
        :disabled="disabled || sending"
        clearable
        style="flex: 1;"
        class="ascii-input"
      >
        <template #prefix>
          <span class="tx-prefix">TX&gt;</span>
        </template>
      </el-input>

      <template v-if="showReadSize">
        <el-input-number
          :model-value="readSize"
          :min="0" :max="256"
          :step="1"
          size="small"
          style="width: 120px;"
          controls-position="right"
          placeholder="RX 字节数"
          @update:model-value="onReadSizeUpdate"
        />
        <span class="read-size-hint" v-if="readSize === 0">仅写</span>
        <span class="read-size-hint" v-else>读 {{ readSize }}B</span>
      </template>

      <el-button
        type="primary"
        size="small"
        @click="emit('send')"
        :loading="sending"
        :disabled="disabled || (!inputData.trim() && !inputDataAscii.trim())"
      >
        发送
      </el-button>
    </div>
  </el-card>
</template>

<script setup lang="ts">
/**
 * 通道终端的「命令输入」子组件（2026-11-12 纯搬移拆分，行为零变化）。
 *
 * 边界依据：
 *   · 搬入的是**自包含模板片段**（发送卡：HEX/ASCII 模式切换、两个输入框、RX 字节数、
 *     发送按钮）与它的专属 scoped 样式。DOM 层级、class、placeholder、v-if 分支逐字保留
 *     （ChannelTerminalHardwareType.spec.ts 用 input.el-input-number 是否存在判定读长度控件）。
 *   · 状态**不**搬：inputMode / inputData / inputDataAscii / readSize 仍由父组件（会话组合式）
 *     持有，这里全部走 props + update:xxx 透传。因此父组件的「切换通道清空输入」
 *     「↑↓ 命令历史」「发送成功后清空」「collector 变化复位」四条既有行为完全不用改。
 *   · 发送动作也不在这里实现：只 emit('send')，由父组件的 sendData 保持原链路
 *     （WS 优先 / HTTP 回退 / 代际校验）。
 *   · 清空语义要逐字保留：Element Plus 的 radio / input-number 在清空时 emit 的值不在
 *     声明类型里（input-number 实际 emit null），原先 v-model 会原样写进 ref。这里同样
 *     原样透传 —— 下面的类型断言只统一 TS 口径，运行时不改写值。
 */
interface Props {
  inputMode: 'hex' | 'ascii'
  inputData: string
  inputDataAscii: string
  readSize: number
  showReadSize: boolean
  sending: boolean
  /** 未选通道时禁用输入（父级传 !selectedChannelId） */
  disabled: boolean
}

defineProps<Props>()

const emit = defineEmits<{
  'update:inputMode': [value: 'hex' | 'ascii']
  'update:inputData': [value: string]
  'update:inputDataAscii': [value: string]
  'update:readSize': [value: number]
  send: []
  keydown: [evt: Event | KeyboardEvent]
}>()

// 运行值原样透传（见文件头「清空语义」）：断言不改变运行时行为。
const onModeChange = (value: string | number | boolean | undefined) =>
  emit('update:inputMode', value as 'hex' | 'ascii')

const onReadSizeUpdate = (value: number | undefined) =>
  emit('update:readSize', value as number)

// el-input 的 @keydown 声明为 (evt: Event | KeyboardEvent) => any，入口保持原签名。
const onKeydown = (evt: Event | KeyboardEvent) => emit('keydown', evt)
</script>

<style scoped>
.terminal-send-card { margin: 0; }
.terminal-input { display: flex; align-items: center; gap: 8px; }
.input-mode-toggle { flex-shrink: 0; }
.tx-prefix { color: var(--el-color-primary); font-weight: 600; font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace; font-size: 12px; margin-right: 4px; }
.read-size-hint { font-size: 11px; color: var(--el-text-color-secondary); white-space: nowrap; }

@media (max-width: 768px) {
  .terminal-input {
    align-items: stretch;
    flex-wrap: wrap;
  }

  .input-mode-toggle,
  .terminal-input :deep(.el-input),
  .terminal-input :deep(.el-input-number) {
    flex: 1 1 100%;
    width: 100% !important;
  }

  .terminal-input :deep(.el-button) {
    flex: 1 1 auto;
  }
}
</style>
