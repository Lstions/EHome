import { defineComponent, nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import RealtimeDataList from '@/components/data/RealtimeDataList.vue'
import type { DataItem } from '@/types/realtime'

const RadioGroupStub = defineComponent({
  name: 'ElRadioGroup',
  props: {
    modelValue: String,
  },
  emits: ['update:modelValue'],
  template: '<div class="el-radio-group"><slot /></div>',
})

const stubs = {
  ElRadioGroup: RadioGroupStub,
  ElRadioButton: { template: '<button class="el-radio-button"><slot /></button>' },
  ElTag: { template: '<span class="el-tag"><slot /></span>' },
  ElButton: {
    props: ['disabled'],
    emits: ['click'],
    template: '<button class="el-button" :disabled="disabled" @click="$emit(\'click\')"><slot /></button>',
  },
  ElEmpty: { template: '<div class="el-empty" />' },
}

const items: DataItem[] = [
  {
    id: 'reading-1',
    timestamp: '2026-07-13T06:00:00Z',
    data: { voltage: 12.5 },
    rawData: [0x01, 0xaf],
    isRealtime: true,
  },
  {
    id: 'reading-2',
    timestamp: '2026-07-13T05:59:00Z',
    data: { voltage: 12.4 },
    isRealtime: false,
  },
]

function mountList(props: { items?: DataItem[]; autoScroll?: boolean } = {}): VueWrapper {
  return mount(RealtimeDataList, {
    attachTo: document.body,
    props: {
      items: props.items ?? items,
      autoScroll: props.autoScroll ?? true,
    },
    global: { stubs },
  })
}

describe('RealtimeDataList with vue-virtual-scroller 3', () => {
  let wrappers: VueWrapper[] = []

  beforeEach(() => {
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(400)
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(800)
  })

  afterEach(() => {
    wrappers.forEach(wrapper => wrapper.unmount())
    wrappers = []
    vi.restoreAllMocks()
  })

  const track = (wrapper: VueWrapper) => {
    wrappers.push(wrapper)
    return wrapper
  }

  it('renders fixed-size rows through the real RecycleScroller component', async () => {
    const wrapper = track(mountList())
    await nextTick()

    expect(wrapper.find('.vue-recycle-scroller').exists()).toBe(true)
    expect(wrapper.findAll('.data-item')).toHaveLength(2)
    expect(wrapper.text()).toContain('voltage: 12.50')
    expect(wrapper.text()).toContain('实时')
    expect(wrapper.text()).toContain('历史')
  })

  it('switches to hexadecimal raw data and emits clear', async () => {
    const wrapper = track(mountList())

    wrapper.findComponent(RadioGroupStub).vm.$emit('update:modelValue', 'hex')
    await nextTick()

    expect(wrapper.find('.item-content').text()).toBe('01 AF')
    await wrapper.find('.list-stats .el-button').trigger('click')
    expect(wrapper.emitted('clear')).toHaveLength(1)
  })

  it('scrolls back to the first row when a new item arrives', async () => {
    const wrapper = track(mountList())
    await nextTick()
    const scroller = wrapper.find('.scroller').element as HTMLElement
    scroller.scrollTop = 160

    await wrapper.setProps({
      items: [
        {
          id: 'reading-3',
          timestamp: '2026-07-13T06:01:00Z',
          data: { voltage: 12.6 },
          isRealtime: true,
        },
        ...items,
      ],
    })
    await nextTick()

    expect(scroller.scrollTop).toBe(0)
  })
})
