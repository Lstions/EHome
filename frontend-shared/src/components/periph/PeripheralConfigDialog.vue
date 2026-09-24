<!--
  PeripheralConfigDialog.vue — GPIO / PWM 配置的创建与编辑对话框

  ## 为什么抽这个组件（而不是在 NodeOverview 里再写一份表单）

  背景：GPIO/PWM 的配置**创建/编辑**入口在整仓只存在于 `components/node/ChannelPanel.vue`
  （`gpioApi.create/update` 与 `pwmApi.create/update` 的全部调用点），而 ChannelPanel 本身
  只被死文件 `NodeDetail.vue` 引用 ⇒ 该能力在生产侧**对用户不可达**。
  设备上报的资源能在「外设控制」TAB 列出，但每个引脚只能点「配置」而无处可去。

  本轮的选择：**抽成共享组件**，让 ChannelPanel（待迁移的旧路径）与
  PeripheralControl（存活路径）**共用同一份表单实现**。
  这样做的关键理由是可逆性 ——
    - 若在 NodeOverview 里复制一份表单，会立刻制造"第二套真相"：两处字段、两处校验、
      两处文案，将来后端 manifest 校验一改就要改两处（本仓已有大量此类教训）；
    - 抽共享组件后，`ChannelPanel` 将来被删除时**不会带走任何能力**，
      死代码清理（C5）从而从"能力迁移"退化为"纯删除"。

  ## 设计约束（不可随意简化）

  1. **代际校验（generation）必须留在父组件**：提交是异步的，节点切换/组件卸载后
     回来的响应不得再改 UI。本组件只负责表单与提交，不做代际判断 ——
     由父组件在 `submit` 回调里用 `requestId`/代际快照决定是否消费结果。
  2. **409 冲突是正常业务结果，不是错误**：后端对同 pin 重复配置返回
     `already configured`/`Conflict`/`409`（`ChannelPanel.vue:1213` 原有判据）。
     这里必须保留该分支，否则用户看到的是红色报错而不是"已存在配置"。
  3. **PWM 分辨率上限来自设备上报**：`maxResolution` 由父组件传入，
     不在本组件内硬编码 —— 不同设备的 PWM 位宽不同。
-->
<template>
  <!-- GPIO：引脚/PWM 资源标识由外部给定且不可改（它就是本对话框的主键） -->
  <el-dialog
    v-model="visible"
    :title="isGpio ? (editing ? '编辑 GPIO 引脚' : '配置 GPIO 引脚') : (editing ? '编辑 PWM 硬件资源' : '配置 PWM 硬件资源')"
    width="460px"
    destroy-on-close
    @closed="emit('closed')"
  >
    <el-form v-if="isGpio" :model="gpioForm" label-width="90px">
      <el-form-item label="引脚">
        <el-input :model-value="`GPIO${gpioForm.pin}`" disabled style="width: 100%;" />
      </el-form-item>
      <el-form-item label="方向">
        <el-select v-model="gpioForm.direction" style="width: 100%;" data-testid="gpio-direction">
          <el-option :value="1" label="输出 (OUTPUT)" />
          <el-option :value="0" label="输入 (INPUT)" />
          <el-option :value="2" label="上拉输入 (INPUT_PULLUP)" />
          <el-option :value="3" label="下拉输入 (INPUT_PULLDOWN)" />
        </el-select>
      </el-form-item>
      <el-form-item v-if="gpioForm.direction === 1" label="初始电平">
        <el-radio-group v-model="gpioForm.initial_level">
          <el-radio :value="0">低电平 (0)</el-radio>
          <el-radio :value="1">高电平 (1)</el-radio>
        </el-radio-group>
      </el-form-item>
      <el-form-item label="标签">
        <el-input v-model="gpioForm.label" placeholder="可选，如：继电器" />
      </el-form-item>
    </el-form>

    <el-form v-else :model="pwmForm" label-width="100px">
      <el-form-item label="PWM 资源">
        <el-input :model-value="`${pwmForm.hardware_id} (channel ${pwmForm.channel})`" disabled style="width: 100%;" />
      </el-form-item>
      <el-form-item label="输出引脚">
        <el-select v-model="pwmForm.pin" placeholder="选择未占用 GPIO" style="width: 100%;" data-testid="pwm-pin">
          <el-option v-for="pin in availablePins" :key="pin" :value="pin" :label="`GPIO${pin}`" />
        </el-select>
      </el-form-item>
      <el-form-item label="频率 (Hz)">
        <el-input-number v-model="pwmForm.frequency" :min="1" :max="40000000" style="width: 100%;" placeholder="如 1000" />
      </el-form-item>
      <el-form-item label="占空比">
        <el-slider v-model="pwmForm.duty" :min="0" :max="10000" :step="10" show-input :show-tooltip="false" />
        <div class="pwm-duty-hint">0-10000 (0.00% - 100.00%)，当前: {{ (pwmForm.duty / 100).toFixed(2) }}%</div>
      </el-form-item>
      <el-form-item label="分辨率">
        <el-input-number v-model="pwmForm.resolution" :min="4" :max="maxResolution" style="width: 100%;" />
        <div class="pwm-duty-hint">4-{{ maxResolution }} bit，默认 14</div>
      </el-form-item>
      <el-form-item label="自动启动">
        <el-switch v-model="pwmForm.auto_start" />
      </el-form-item>
      <el-form-item label="标签">
        <el-input v-model="pwmForm.label" placeholder="可选" />
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button
        :data-testid="isGpio ? 'submit-gpio' : 'submit-pwm'"
        type="primary"
        :loading="saving"
        @click="onSubmit"
      >{{ editing ? '确认保存' : '确认添加' }}</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'

/** GPIO 表单（与 ChannelPanel 同形，字段名保持一致以免迁移时语义漂移） */
export interface GpioFormModel {
  pin: number
  direction: number
  initial_level: number
  label: string
}
/** PWM 表单 */
export interface PwmFormModel {
  hardware_id: string
  channel: number
  pin: number
  frequency: number
  duty: number
  resolution: number
  auto_start: boolean
  label: string
}

const props = withDefaults(defineProps<{
  modelValue: boolean
  /** 'gpio' | 'pwm' */
  kind: 'gpio' | 'pwm'
  /** 是否编辑已有配置（true=update，false=create） */
  editing?: boolean
  /** GPIO 初始值 */
  gpio?: GpioFormModel
  /** PWM 初始值 */
  pwm?: PwmFormModel
  /** PWM 可选输出引脚（来自设备上报/占用计算） */
  availablePins?: number[]
  /** PWM 分辨率上限（设备能力，不得硬编码） */
  maxResolution?: number
  /** 提交中（由父组件持有，避免本组件与父组件的忙态分叉） */
  saving?: boolean
}>(), {
  editing: false,
  availablePins: () => [],
  maxResolution: 14,
  saving: false,
})

const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  /** 提交：父组件负责调 API 与代际校验；返回 false 表示失败（本组件不关闭） */
  (e: 'submit', payload: GpioFormModel | PwmFormModel): void
  (e: 'closed'): void
}>()

const visible = computed({
  get: () => props.modelValue,
  set: v => emit('update:modelValue', v),
})

const isGpio = computed(() => props.kind === 'gpio')

const DEFAULT_GPIO: GpioFormModel = { pin: 5, direction: 1, initial_level: 0, label: '' }
const DEFAULT_PWM: PwmFormModel = {
  hardware_id: '', channel: 0, pin: -1, frequency: 1000,
  duty: 0, resolution: 14, auto_start: false, label: '',
}

const gpioForm = reactive<GpioFormModel>({ ...DEFAULT_GPIO })
const pwmForm = reactive<PwmFormModel>({ ...DEFAULT_PWM })

// 打开时用父组件传入的初值填充；`destroy-on-close` 之外再做一次显式同步，
// 避免"上次编辑的值残留到下次创建"（ChannelPanel 原实现靠 @closed 重置，等价）。
watch(() => props.modelValue, (open) => {
  if (!open) return
  if (props.kind === 'gpio') {
    Object.assign(gpioForm, DEFAULT_GPIO, props.gpio ?? {})
  } else {
    Object.assign(pwmForm, DEFAULT_PWM, props.pwm ?? {})
  }
}, { immediate: true })

function onSubmit() {
  emit('submit', isGpio.value ? { ...gpioForm } : { ...pwmForm })
}
</script>

<style scoped>
.pwm-duty-hint { font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.4; }
</style>
