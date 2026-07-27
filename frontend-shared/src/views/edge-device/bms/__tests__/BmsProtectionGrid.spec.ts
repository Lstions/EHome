import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import BmsProtectionGrid from '@/views/edge-device/bms/BmsProtectionGrid.vue'

// Stubs
const TagStub = defineComponent({
  props: ['type', 'size', 'effect'],
  template: '<span class="el-tag"><slot /></span>',
})
const EmptyStub = defineComponent({
  props: ['description', 'imageSize'],
  template: '<div class="el-empty">{{ description }}</div>',
})
const IconStub = defineComponent({
  props: ['size', 'color'],
  template: '<span class="el-icon"><slot /></span>',
})
const WarningFilled = defineComponent({ template: '<span class="warning-icon" />' })
const CircleCheck = defineComponent({ template: '<span class="check-icon" />' })

const stubs = {
  'el-tag': TagStub,
  'el-empty': EmptyStub,
  'el-icon': IconStub,
  WarningFilled,
  CircleCheck,
}

const global = { stubs }

describe('BmsProtectionGrid', () => {
  it('shows protection items when bitmask has active bits', () => {
    const wrapper = mount(BmsProtectionGrid, {
      props: { data: { protection_status: 0b11 } }, // bit0 + bit1
      global,
    })
    expect(wrapper.text()).toContain('单体过压')
    expect(wrapper.text()).toContain('单体欠压')
    expect(wrapper.find('.protection-summary').exists()).toBe(false)
    expect(wrapper.find('.el-empty').exists()).toBe(false)
  })

  it('shows "保护状态正常" summary tag when bitmask is 0', () => {
    const wrapper = mount(BmsProtectionGrid, {
      props: { data: { protection_status: 0 } },
      global,
    })
    expect(wrapper.find('.protection-summary').exists()).toBe(true)
    expect(wrapper.text()).toContain('保护状态正常')
    // el-empty fallback should NOT appear when data is present
    expect(wrapper.find('.el-empty').exists()).toBe(false)
  })

  it('shows el-empty "无保护状态数据" when data is null', () => {
    // FIX for finding 14: previously the summary slot existed but rendered
    // nothing (v-if=false), suppressing the built-in el-empty.
    // Now the summary slot includes an <el-empty v-else> fallback.
    const wrapper = mount(BmsProtectionGrid, {
      props: { data: null },
      global,
    })
    expect(wrapper.find('.el-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('无保护状态数据')
    expect(wrapper.find('.protection-summary').exists()).toBe(false)
  })

  it('shows el-empty when data has no protection_status field', () => {
    const wrapper = mount(BmsProtectionGrid, {
      props: { data: { voltage: 12.5 } }, // no protection_status
      global,
    })
    expect(wrapper.find('.el-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('无保护状态数据')
  })

  it('decodes all 12 protection bits correctly', () => {
    // All bits set
    const wrapper = mount(BmsProtectionGrid, {
      props: { data: { protection_status: 0xFFF } },
      global,
    })
    const labels = ['单体过压', '单体欠压', '整组过压', '整组欠压',
      '充电过温', '充电低温', '放电过温', '放电低温',
      '充电过流', '放电过流', '短路保护', '前端IC错误']
    for (const label of labels) {
      expect(wrapper.text()).toContain(label)
    }
  })

  it('uses individual protection_* fields when no bitmask', () => {
    const wrapper = mount(BmsProtectionGrid, {
      props: {
        data: {
          protection_overvoltage: 1,
          protection_overcurrent_discharge: 0,
        },
      },
      global,
    })
    expect(wrapper.text()).toContain('过压保护')
    expect(wrapper.text()).toContain('放电过流')
  })
})
