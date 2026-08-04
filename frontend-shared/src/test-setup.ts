import { defineComponent, h, inject, provide, type Component, type InjectionKey } from 'vue'
import { config } from '@vue/test-utils'

/**
 * 全局 Element Plus 组件 stub 注册
 *
 * 根因：vitest.config.ts 的 vue() 插件没有 unplugin-vue-components，
 * 导致 SFC 中 <el-tag> 等被编译器视为原生自定义元素而非组件。
 * Vue Test Utils 的 stubs 只匹配组件名，不匹配原生元素。
 *
 * 方案：用 global.components 注册 PascalCase 名的 stub，
 * Vue 编译器会将 <el-tag> 解析为已注册的 ElTag 组件。
 *
 * 所有 stub 都使用 class 名（如 "el-tag"）而非 data-testid，
 * 以兼容现有测试的选择器。
 */

// --- 基础交互组件 ---

const ElButton = defineComponent({
  props: {
    type: { type: String, default: 'default' },
    size: { type: String, default: 'default' },
    plain: Boolean,
    round: Boolean,
    circle: Boolean,
    loading: Boolean,
    disabled: Boolean,
    icon: [String, Object],
    autofocus: Boolean,
    nativeType: { type: String, default: 'button' },
  },
  emits: ['click'],
  setup(props, { slots, emit }) {
    return () => h('button', {
      class: ['el-button', `el-button--${props.type}`, props.size ? `el-button--${props.size}` : ''],
      'data-loading': String(Boolean(props.loading)),
      disabled: props.disabled || props.loading,
      onClick: (e) => emit('click', e),
    }, [
      props.icon ? h(props.icon as any, { class: 'el-button__icon' }) : null,
      slots.default?.(),
    ])
  },
})

const ElInput = defineComponent({
  props: {
    modelValue: { type: [String, Number], default: '' },
    type: { type: String, default: 'text' },
    size: String,
    disabled: Boolean,
    clearable: Boolean,
    showPassword: Boolean,
    placeholder: String,
    maxlength: [String, Number],
    rows: { type: Number, default: 2 },
    autosize: [Boolean, Object],
  },
  emits: ['update:modelValue', 'input', 'change', 'blur', 'focus', 'clear'],
  setup(props, { emit, attrs }) {
    const onInput = (e: Event) => {
      const val = (e.target as HTMLInputElement).value
      emit('update:modelValue', val)
      emit('input', val)
    }
    const onBlur = (e: Event) => emit('blur', e)
    const onFocus = (e: Event) => emit('focus', e)
    return () => {
      if (props.type === 'textarea') {
        return h('textarea', {
          class: 'el-textarea__inner',
          value: props.modelValue,
          placeholder: props.placeholder,
          disabled: props.disabled,
          rows: props.rows,
          onInput,
          onBlur,
          onFocus,
          ...attrs,
        })
      }
      return h('input', {
        class: 'el-input__inner',
        type: props.showPassword ? 'password' : props.type,
        value: props.modelValue,
        placeholder: props.placeholder,
        disabled: props.disabled,
        maxlength: props.maxlength,
        onInput,
        onBlur,
        onFocus,
        ...attrs,
      })
    }
  },
})

const ElSelect = defineComponent({
  props: {
    modelValue: { type: [String, Number, Array], default: '' },
    disabled: Boolean,
    clearable: Boolean,
    multiple: Boolean,
    size: String,
    placeholder: String,
    filterable: Boolean,
  },
  emits: ['update:modelValue', 'change', 'blur', 'focus', 'visible-change'],
  setup(props, { slots, emit, attrs }) {
    const onChange = (e: Event) => {
      const value = (e.target as HTMLSelectElement).value
      const normalized = value === '' ? undefined : (/^-?\d+(?:\.\d+)?$/.test(value) ? Number(value) : value)
      emit('update:modelValue', normalized)
      emit('change', normalized)
    }
    return () => h('select', {
      class: 'el-select',
      'aria-label': attrs['aria-label'] as string,
      value: String(props.modelValue ?? ''),
      disabled: props.disabled,
      onChange,
    }, [
      h('option', { value: '' }, props.placeholder || ''),
      slots.default?.(),
    ])
  },
})

const ElOption = defineComponent({
  props: {
    value: { type: [String, Number, Object], required: true },
    label: String,
    disabled: Boolean,
  },
  setup(props) {
    return () => h('option', { class: 'el-option', value: String(props.value), disabled: props.disabled }, props.label ?? String(props.value))
  },
})

const ElCheckbox = defineComponent({
  props: {
    modelValue: { type: [Boolean, Array], default: false },
    label: String,
    disabled: Boolean,
    indeterminate: Boolean,
  },
  emits: ['update:modelValue', 'change'],
  setup(props, { slots, emit }) {
    return () => h('label', { class: 'el-checkbox' }, [
      h('input', {
        type: 'checkbox',
        checked: Array.isArray(props.modelValue) ? props.modelValue.includes(props.label) : props.modelValue,
        disabled: props.disabled,
        onChange: (e: Event) => {
          const checked = (e.target as HTMLInputElement).checked
          emit('update:modelValue', checked)
          emit('change', checked)
        },
      }),
      h('span', { class: 'el-checkbox__label' }, slots.default?.()),
    ])
  },
})

// --- 展示型组件 ---

const ElTag = defineComponent({
  props: {
    type: { type: String, default: '' },
    size: { type: String, default: 'default' },
    effect: { type: String, default: 'light' },
    closable: Boolean,
    round: Boolean,
  },
  setup(props, { slots }) {
    return () => h('span', {
      class: ['el-tag', `el-tag--${props.type}`, `el-tag--${props.size}`, `el-tag--${props.effect}`],
      'data-type': props.type,
      'data-effect': props.effect,
    }, slots.default?.())
  },
})

const ElIcon = defineComponent({
  props: {
    size: { type: [String, Number], default: '' },
    color: String,
  },
  setup(props, { slots }) {
    return () => h('span', {
      class: 'el-icon',
      style: { fontSize: typeof props.size === 'number' ? `${props.size}px` : props.size, color: props.color },
    }, slots.default?.())
  },
})

const ElEmpty = defineComponent({
  props: {
    description: { type: String, default: '暂无数据' },
    imageSize: { type: Number, default: 0 },
    image: String,
  },
  setup(props, { slots }) {
    return () => h('div', { class: 'el-empty' }, [
      slots.image?.() ?? h('div', { class: 'el-empty__image' }),
      h('p', { class: 'el-empty__description' }, props.description),
      slots.default?.(),
    ])
  },
})

const ElDialog = defineComponent({
  props: {
    modelValue: { type: Boolean, default: false },
    title: String,
    width: { type: String, default: '50%' },
    fullscreen: Boolean,
    showClose: { type: Boolean, default: true },
    beforeClose: Function,
    appendToBody: Boolean,
    modal: { type: Boolean, default: true },
  },
  emits: ['update:modelValue', 'close', 'open', 'closed', 'opened'],
  setup(props, { slots }) {
    return () => props.modelValue ? h('div', {
      class: 'el-dialog__wrapper',
    }, [
      h('div', {
        class: 'el-dialog',
        role: 'dialog',
        style: { width: props.width },
      }, [
        h('div', { class: 'el-dialog__header' }, [
          h('span', { class: 'el-dialog__title' }, props.title),
        ]),
        h('div', { class: 'el-dialog__body' }, slots.default?.()),
        slots.footer ? h('div', { class: 'el-dialog__footer' }, slots.footer()) : null,
      ]),
    ]) : null
  },
})

const ElCard = defineComponent({
  props: {
    header: String,
    shadow: { type: String, default: 'always' },
    bodyStyle: [String, Object],
  },
  setup(props, { slots }) {
    return () => h('div', { class: ['el-card', `is-${props.shadow}-shadow`] }, [
      slots.header ? h('div', { class: 'el-card__header' }, slots.header()) : (props.header ? h('div', { class: 'el-card__header' }, props.header) : null),
      h('div', { class: 'el-card__body' }, slots.default?.()),
    ])
  },
})

const ElTooltip = defineComponent({
  props: {
    content: String,
    placement: { type: String, default: 'top' },
    effect: { type: String, default: 'dark' },
    disabled: Boolean,
    visible: Boolean,
  },
  setup(_, { slots }) {
    return () => h('span', { class: 'el-tooltip' }, slots.default?.())
  },
})

const ElTable = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
    border: Boolean,
    stripe: Boolean,
    height: [String, Number],
    rowKey: [String, Function],
  },
  emits: ['selection-change', 'row-click', 'sort-change'],
  setup(props, { slots, emit }) {
    // 行选中状态 (对象身份集合); type="selection" 列存在时每行渲染 checkbox,
    // 勾选切换触发 selection-change, 与真实 el-table 的多选契约一致。
    const selectedRows = new Set<any>()
    return () => {
      const columnVNodes = slots.default?.() ?? []
      const flat = Array.isArray(columnVNodes) ? columnVNodes : [columnVNodes]
      const hasSelection = flat.some((vn: any) => vn?.props?.type === 'selection')
      return h('table', { class: 'el-table' }, [
        columnVNodes,
        // 轻量环境中保留 row 文本，使历史记录等表格测试仍可验证数据呈现。
        h('tbody', props.data.map((row: any, index: number) => h('tr', { key: row?.id ?? index }, [
          hasSelection ? h('td', { class: 'el-table__cell el-table__selection-cell' }, [
            h('input', {
              type: 'checkbox',
              class: 'el-table__row-checkbox',
              'aria-label': `选择行 ${row?.id ?? index}`,
              checked: selectedRows.has(row),
              onChange: (event: Event) => {
                const checked = (event.target as HTMLInputElement).checked
                if (checked) selectedRows.add(row)
                else selectedRows.delete(row)
                emit('selection-change', Array.from(selectedRows))
              },
            }),
          ]) : null,
          h('td', { class: 'el-table__cell' }, Object.values(row ?? {}).map(String).join(' ')),
        ]))),
      ])
    }
  },
})

const ElTableColumn = defineComponent({
  props: {
    prop: String,
    label: String,
    width: [String, Number],
    minWidth: [String, Number],
    fixed: [String, Boolean],
    type: String,
    align: String,
    sortable: Boolean,
  },
  setup() {
    return () => h('td', { class: 'el-table-column' })
  },
})

const ElPagination = defineComponent({
  props: {
    total: { type: Number, default: 0 },
    currentPage: { type: Number, default: 1 },
    pageSize: { type: Number, default: 10 },
    pageSizes: { type: Array, default: () => [10, 20, 50, 100] },
    layout: { type: String, default: 'prev, pager, next' },
  },
  emits: ['update:currentPage', 'size-change', 'current-change'],
  setup(props, { emit, attrs }) {
    return () => h('button', {
      class: 'el-pagination',
      'aria-label': attrs['aria-label'] as string,
      onClick: () => {
        const next = props.currentPage + 1
        emit('update:currentPage', next)
        emit('current-change', next)
      },
    }, `共 ${props.total} 条`)
  },
})

const ElSwitch = defineComponent({
  props: {
    modelValue: { type: [Boolean, String, Number], default: false },
    disabled: Boolean,
    activeText: String,
    inactiveText: String,
    activeValue: { default: true },
    inactiveValue: { default: false },
  },
  emits: ['update:modelValue', 'change', 'input'],
  setup(props, { emit }) {
    return () => h('button', {
      class: ['el-switch', { 'is-checked': props.modelValue === props.activeValue }],
      disabled: props.disabled,
      onClick: () => {
        const val = props.modelValue === props.activeValue ? props.inactiveValue : props.activeValue
        emit('update:modelValue', val)
        emit('change', val)
        emit('input', val)
      },
    })
  },
})

const ElAlert = defineComponent({
  props: {
    title: String,
    type: { type: String, default: 'info' },
    description: String,
    closable: { type: Boolean, default: true },
    showIcon: Boolean,
    center: Boolean,
    effect: { type: String, default: 'light' },
  },
  setup(props, { slots }) {
    return () => h('div', { class: ['el-alert', `el-alert--${props.type}`] }, [
      h('div', { class: 'el-alert__content' }, [
        // 支持 #title slot 和 title prop 两种用法
        slots.title ? h('div', { class: 'el-alert__title' }, slots.title()) : (props.title ? h('div', { class: 'el-alert__title' }, props.title) : null),
        slots.default?.() || (props.description ? h('div', { class: 'el-alert__description' }, props.description) : null),
      ]),
    ])
  },
})

const ElDivider = defineComponent({
  props: {
    direction: { type: String, default: 'horizontal' },
    borderStyle: { type: String, default: 'solid' },
  },
  setup() {
    return () => h('div', { class: 'el-divider' })
  },
})

const ElRow = defineComponent({
  props: {
    gutter: { type: Number, default: 0 },
    justify: { type: String, default: 'start' },
    align: { type: String, default: 'top' },
  },
  setup(props, { slots }) {
    return () => h('div', { class: ['el-row', `is-justify-${props.justify}`, `is-align-${props.align}`] }, slots.default?.())
  },
})

const ElCol = defineComponent({
  props: {
    span: { type: Number, default: 24 },
    offset: { type: Number, default: 0 },
    push: { type: Number, default: 0 },
    pull: { type: Number, default: 0 },
    xs: [Number, Object],
    sm: [Number, Object],
    md: [Number, Object],
    lg: [Number, Object],
    xl: [Number, Object],
  },
  setup(props, { slots }) {
    return () => h('div', {
      class: ['el-col', `el-col-${props.span}`],
    }, slots.default?.())
  },
})

const ElDropdown = defineComponent({
  props: {
    trigger: { type: String, default: 'hover' },
    type: String,
    size: String,
    splitButton: Boolean,
  },
  emits: ['command', 'visible-change', 'click'],
  setup(_, { slots }) {
    return () => h('div', { class: 'el-dropdown' }, [
      slots.default?.(),
      slots.dropdown?.(),
    ])
  },
})

const ElDropdownMenu = defineComponent({
  setup(_, { slots }) {
    return () => h('div', { class: 'el-dropdown-menu' }, slots.default?.())
  },
})

const ElDropdownItem = defineComponent({
  props: { command: [String, Number, Object], disabled: Boolean, divided: Boolean },
  emits: ['click'],
  setup(props, { slots, emit }) {
    return () => h('div', {
      class: ['el-dropdown-menu__item', { 'is-disabled': props.disabled }],
      'data-command': String(props.command),
      onClick: () => emit('click'),
    }, slots.default?.())
  },
})

const ElForm = defineComponent({
  props: {
    model: Object,
    rules: Object,
    labelWidth: String,
    labelPosition: { type: String, default: 'right' },
    inline: Boolean,
    disabled: Boolean,
  },
  emits: ['validate'],
  setup(_, { slots }) {
    return () => h('form', { class: 'el-form' }, slots.default?.())
  },
})

const ElFormItem = defineComponent({
  props: {
    label: String,
    prop: String,
    required: Boolean,
    rules: [Object, Array],
    error: String,
    labelWidth: String,
  },
  setup(props, { slots }) {
    return () => h('div', { class: 'el-form-item' }, [
      props.label ? h('label', { class: 'el-form-item__label' }, props.label) : null,
      h('div', { class: 'el-form-item__content' }, slots.default?.()),
    ])
  },
})

const ElProgress = defineComponent({
  props: {
    percentage: { type: Number, default: 0 },
    type: { type: String, default: 'line' },
    status: String,
    strokeWidth: { type: Number, default: 6 },
    color: [String, Array, Function],
  },
  setup(props) {
    return () => h('div', { class: 'el-progress' }, [
      h('div', { class: 'el-progress-bar' }, [
        h('div', { class: 'el-progress-bar__outer', style: { width: '100%' } }, [
          h('div', { class: 'el-progress-bar__inner', style: { width: `${props.percentage}%` } }),
        ]),
      ]),
    ])
  },
})

// ElRadioGroup/ElRadio — 交互式 stub (provide/inject):
// group 提供当前值 + 选中回调; radio 渲染真实 <input type="radio">,
// checked 反映 group 当前值, click/change 触发 update:modelValue + change。
// 支持 Element Plus 2.x 的 value prop (新) 与 label prop (旧) 双写法。
interface RadioGroupContext {
  currentValue: () => string | number | boolean | undefined
  disabled: () => boolean
  select: (value: string | number | boolean) => void
}
const RADIO_GROUP_KEY: InjectionKey<RadioGroupContext> = Symbol('el-radio-group-stub')

const ElRadioGroup = defineComponent({
  props: { modelValue: [String, Number, Boolean], disabled: Boolean },
  emits: ['update:modelValue', 'change'],
  setup(props, { slots, emit }) {
    provide(RADIO_GROUP_KEY, {
      currentValue: () => props.modelValue,
      disabled: () => props.disabled,
      select: (value) => {
        emit('update:modelValue', value)
        emit('change', value)
      },
    })
    return () => h('div', { class: 'el-radio-group', role: 'radiogroup' }, slots.default?.())
  },
})

const ElRadio = defineComponent({
  props: {
    label: [String, Number, Boolean],
    value: [String, Number, Boolean],
    disabled: Boolean,
    modelValue: [String, Number, Boolean],
  },
  emits: ['update:modelValue', 'change'],
  setup(props, { slots, emit }) {
    const group = inject(RADIO_GROUP_KEY, null)
    return () => {
      // EP 2.x: value 优先, label 兼容旧写法
      const ownValue = props.value !== undefined ? props.value : props.label
      const current = group ? group.currentValue() : props.modelValue
      const checked = current === ownValue
      const disabled = props.disabled || (group ? group.disabled() : false)
      const select = () => {
        if (disabled) return
        if (group) group.select(ownValue as string | number | boolean)
        else {
          emit('update:modelValue', ownValue)
          emit('change', ownValue)
        }
      }
      return h('label', { class: ['el-radio', checked ? 'is-checked' : ''] }, [
        h('input', {
          type: 'radio',
          class: 'el-radio__input',
          checked,
          disabled,
          onClick: select,
          onChange: select,
        }),
        h('span', { class: 'el-radio__label' }, slots.default?.()),
      ])
    }
  },
})

// --- 注册全部 stub ---

const elStubs: Record<string, Component> = {
  ElButton,
  ElInput,
  ElSelect,
  ElOption,
  ElCheckbox,
  ElTag,
  ElIcon,
  ElEmpty,
  ElDialog,
  ElCard,
  ElTooltip,
  ElTable,
  ElTableColumn,
  ElPagination,
  ElSwitch,
  ElAlert,
  ElDivider,
  ElRow,
  ElCol,
  ElDropdown,
  ElDropdownMenu,
  ElDropdownItem,
  ElForm,
  ElFormItem,
  ElProgress,
  ElRadioGroup,
  ElRadio,
}

// 全局注册，使 <el-*> 在 SFC 中被解析为组件
for (const [name, comp] of Object.entries(elStubs)) {
  config.global.components[name] = comp
  // 同时注册 kebab-case 别名
  const kebab = name.replace(/([A-Z])/g, '-$1').toLowerCase().replace(/^-/, '')
  config.global.components[kebab] = comp
}

// --- 额外注册项目中用到但未单独定义的 Element Plus 组件 ---
// 使用通用 stub：渲染为一个带 class 的 div/span，传递 slot 内容
const genericElComponents = [
  'ElAside', 'ElAvatar', 'ElBadge', 'ElBreadcrumb', 'ElBreadcrumbItem',
  'ElContainer', 'ElDrawer', 'ElHeader', 'ElMain',
  'ElPopover', 'ElScrollbar', 'ElCascader', 'ElCollapse', 'ElCollapseItem',
  'ElDescriptions',
  'ElLink', 'ElOptionGroup', 'ElRadioButton', 'ElResult', 'ElSkeleton',
  'ElStep', 'ElSteps', 'ElTabPane', 'ElTabs', 'ElTimeline',
  'ElTimelineItem', 'ElUpload', 'ElButtonGroup',
]

// ElMenu/ElMenuItem 需要特殊处理：渲染 class + data-index + emit select
config.global.components['ElMenu'] = defineComponent({
  props: { defaultActive: String, collapse: Boolean, mode: String },
  emits: ['select'],
  setup(_, { slots }) {
    return () => h('div', { class: 'el-menu' }, slots.default?.())
  },
})
config.global.components['el-menu'] = config.global.components['ElMenu']

config.global.components['ElMenuItem'] = defineComponent({
  props: { index: String, disabled: Boolean },
  emits: ['click', 'select'],
  setup(props, { slots }) {
    return () => h('div', { class: 'el-menu-item', 'data-index': props.index }, slots.default?.())
  },
})
config.global.components['el-menu-item'] = config.global.components['ElMenuItem']

// ElSlider 需要渲染 <input> 以便测试通过 aria-label 查找和 trigger change
config.global.components['ElSlider'] = defineComponent({
  props: {
    modelValue: { type: Number, default: 0 },
    min: { type: Number, default: 0 },
    max: { type: Number, default: 100 },
    step: { type: Number, default: 1 },
    disabled: Boolean,
    showTooltip: { type: Boolean, default: true },
  },
  emits: ['update:modelValue', 'change', 'input'],
  setup(props, { emit, attrs }) {
    return () => h('input', {
      type: 'range',
      class: 'el-slider',
      'aria-label': (attrs['aria-label'] as string) || 'duty-slider',
      min: String(props.min),
      max: String(props.max),
      step: String(props.step),
      value: String(props.modelValue),
      disabled: props.disabled,
      onInput: (e: Event) => {
        const val = Number((e.target as HTMLInputElement).value)
        emit('update:modelValue', val)
        emit('input', val)
      },
      onChange: (e: Event) => {
        const val = Number((e.target as HTMLInputElement).value)
        emit('change', val)
      },
    })
  },
})
config.global.components['el-slider'] = config.global.components['ElSlider']

// ElDatePicker 需渲染可编辑 input；日期范围和单点时间均支持 v-model。
config.global.components['ElDatePicker'] = defineComponent({
  props: { modelValue: { type: [String, Number, Array, Object], default: null }, type: String, disabled: Boolean },
  emits: ['update:modelValue', 'change'],
  setup(props, { emit, attrs }) {
    const update = (e: Event) => {
      const value = (e.target as HTMLInputElement).value
      emit('update:modelValue', value === '' ? null : Number(value))
      emit('change', value === '' ? null : Number(value))
    }
    const updateRange = (index: number, e: Event) => {
      const values = Array.isArray(props.modelValue) ? [...props.modelValue] : ['', '']
      const value = (e.target as HTMLInputElement).value
      values[index] = value === '' ? '' : Number(value)
      emit('update:modelValue', values)
      emit('change', values)
    }
    return () => props.type === 'datetimerange'
      ? h('div', { class: 'el-date-picker', 'aria-label': attrs['aria-label'] as string }, [
          h('input', {
            'aria-label': '历史开始时间',
            value: Array.isArray(props.modelValue) ? String(props.modelValue[0] ?? '') : '',
            disabled: props.disabled,
            onInput: (e: Event) => updateRange(0, e),
          }),
          h('input', {
            'aria-label': '历史结束时间',
            value: Array.isArray(props.modelValue) ? String(props.modelValue[1] ?? '') : '',
            disabled: props.disabled,
            onInput: (e: Event) => updateRange(1, e),
          }),
        ])
      : h('input', {
          class: 'el-date-picker',
          'aria-label': attrs['aria-label'] as string,
          value: String(props.modelValue ?? ''),
          disabled: props.disabled,
          onInput: update,
        })
  },
})
config.global.components['el-date-picker'] = config.global.components['ElDatePicker']

// ElInputNumber 使用真实 number input，供配置表单测试和用户输入交互使用。
config.global.components['ElInputNumber'] = defineComponent({
  props: { modelValue: { type: Number, default: 0 }, min: Number, max: Number, step: Number, disabled: Boolean },
  emits: ['update:modelValue', 'change'],
  setup(props, { emit, attrs }) {
    const update = (e: Event) => {
      const value = Number((e.target as HTMLInputElement).value)
      emit('update:modelValue', value)
      emit('change', value)
    }
    return () => h('input', {
      type: 'number',
      class: 'el-input-number',
      value: String(props.modelValue),
      min: props.min == null ? undefined : String(props.min),
      max: props.max == null ? undefined : String(props.max),
      step: props.step == null ? undefined : String(props.step),
      disabled: props.disabled,
      'aria-label': attrs['aria-label'] as string,
      onInput: update,
      onChange: update,
    })
  },
})
config.global.components['el-input-number'] = config.global.components['ElInputNumber']

// ElDescriptionsItem 为内容提供语义容器。
config.global.components['ElDescriptionsItem'] = defineComponent({
  props: { label: String, span: Number },
  setup(props, { slots }) {
    return () => h('div', { class: 'el-descriptions-item' }, [
      props.label ? h('span', { class: 'el-descriptions-item__label' }, props.label) : null,
      h('span', { class: 'el-descriptions-item__content' }, slots.default?.()),
    ])
  },
})
config.global.components['el-descriptions-item'] = config.global.components['ElDescriptionsItem']

for (const name of genericElComponents) {
  if (!config.global.components[name]) {
    config.global.components[name] = defineComponent({
      props: { modelValue: null, disabled: Boolean, loading: Boolean },
      emits: ['update:modelValue', 'change', 'click', 'input', 'blur', 'focus', 'select'],
      setup(_, { slots, attrs }) {
        return () => h('div', { class: name.toLowerCase().replace(/([A-Z])/g, '-$1').replace(/^-/, ''), ...attrs }, slots.default?.())
      },
    })
    const kebab = name.replace(/([A-Z])/g, '-$1').toLowerCase().replace(/^-/, '')
    config.global.components[kebab] = config.global.components[name]
  }
}
