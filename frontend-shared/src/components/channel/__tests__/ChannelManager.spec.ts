import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import ChannelManager from '../ChannelManager.vue'

vi.mock('@/api/channel', () => ({ channelApi: { create: vi.fn(), update: vi.fn() } }))
vi.mock('@/api/node', () => ({ nodeApi: { scanI2C: vi.fn(), syncConfig: vi.fn() } }))

const DialogStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: Boolean, title: String, width: String },
  emits: ['update:modelValue', 'closed'],
  template: '<div class="el-dialog" v-if="modelValue"><slot /><slot name="footer" /></div>',
})

const FormStub = defineComponent({
  props: { model: Object, rules: Object, labelPosition: String, disabled: Boolean },
  setup(_, { expose }) {
    const validate = () => Promise.resolve(true)
    expose({ validate })
  },
  template: '<form><slot /></form>',
})

const FormItemStub = defineComponent({
  props: { label: String, prop: String },
  template: '<section><slot /></section>',
})

const SelectStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: [String, Number], disabled: Boolean, placeholder: String },
  emits: ['update:modelValue', 'change'],
  template: '<div class="el-select" v-bind="$attrs"><slot /></div>',
})

const OptionStub = defineComponent({
  props: { label: [String, Number], value: [String, Number, Boolean] },
  template: '<option :value="value" />',
})

const InputStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: String },
  emits: ['update:modelValue'],
  template: '<input v-bind="$attrs" />',
})

const InputNumberStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: Number, min: Number, max: Number, step: Number },
  emits: ['update:modelValue'],
  template: '<input type="number" v-bind="$attrs" />',
})

const RadioGroupStub = defineComponent({
  inheritAttrs: false,
  props: { modelValue: [String, Number, Boolean] },
  emits: ['update:modelValue'],
  template: '<div v-bind="$attrs"><slot /></div>',
})

const RadioButtonStub = defineComponent({
  props: { value: [String, Number, Boolean] },
  template: '<button><slot /></button>',
})

const SwitchStub = defineComponent({
  props: { modelValue: Boolean },
  emits: ['update:modelValue'],
  template: '<input type="checkbox" :checked="modelValue" />',
})

const ButtonStub = defineComponent({
  inheritAttrs: false,
  props: { type: String, loading: Boolean, disabled: Boolean },
  emits: ['click'],
  template: '<button v-bind="$attrs" @click="$emit(\'click\')"><slot /></button>',
})

const IconStub = defineComponent({
  template: '<i><slot /></i>',
})

const stubs = {
  'el-dialog': DialogStub,
  'el-form': FormStub,
  'el-form-item': FormItemStub,
  'el-select': SelectStub,
  'el-option': OptionStub,
  'el-input': InputStub,
  'el-input-number': InputNumberStub,
  'el-radio-group': RadioGroupStub,
  'el-radio-button': RadioButtonStub,
  'el-switch': SwitchStub,
  'el-button': ButtonStub,
  'el-icon': IconStub,
}

describe('ChannelManager', () => {
  it('keeps UART parameters editable when capability data is temporarily unavailable', async () => {
    const wrapper = mount(ChannelManager, {
      props: {
        modelValue: false,
        collectorId: 'node-1',
        initialData: {
          id: 7,
          hardware_type: 'uart',
          hardware_id: 'UART1',
          name: '现场串口',
          enabled: true,
          config: { baud_rate: 9600, data_bits: 8, stop_bits: 1, parity: 'none' },
        },
      },
      global: { stubs },
    })

    await wrapper.setProps({ modelValue: true })

    expect(wrapper.findAll('input[type="number"]').length).toBeGreaterThan(0)
    expect(wrapper.findAll('button').length).toBeGreaterThanOrEqual(3)
  })
})
