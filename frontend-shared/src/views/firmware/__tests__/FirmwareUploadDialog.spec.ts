import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h } from 'vue'
import FirmwareUploadDialog from '../components/FirmwareUploadDialog.vue'
import { FIRMWARE_ERROR_TEXT } from '@/api/firmware'

/**
 * 上传对话框（M1b / T0.6 + T1b.3）。
 *
 * 三个必须成立的契约：
 *   ① 表单**不再收集 version / target_model**（它们要么由镜像决定，要么列已删）；
 *   ② .bin 与 .meta.json 缺一不可，缺侧车时的提示要与后端 FW_META_REQUIRED 同一句话；
 *   ③ 服务端拒绝（FW_META_CONFLICT 等）以**常驻**错误呈现，而不是一闪而过的 toast。
 */

const mocks = vi.hoisted(() => ({
  upload: vi.fn(),
}))

vi.mock('@/api/firmware', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/firmware')>()
  return {
    ...actual,
    firmwareApi: { ...actual.firmwareApi, upload: mocks.upload },
  }
})

/**
 * el-upload 的替身：真实组件用 `on-change` prop 把用户选中的文件交给调用方。
 * happy-dom 里点不动真实 `<input type=file>`，故按 accept 决定交哪个文件 ——
 * 这正是「两个上传框分别接收 .bin 与 .json」的语义。
 */
const ElUploadStub = defineComponent({
  props: {
    autoUpload: Boolean,
    limit: Number,
    accept: String,
    onChange: Function,
    onExceed: Function,
  },
  setup(props, { slots }) {
    const pick = () => {
      const isMeta = String(props.accept || '').includes('json')
      const file = isMeta
        ? new File(['{"profile":"c6-n8"}'], 'ehome.meta.json', { type: 'application/json' })
        : new File(['\xE9bin'], 'ehome.bin', { type: 'application/octet-stream' })
      props.onChange?.({ name: file.name, raw: file })
    }
    return () => h('div', { class: 'upload-stub', onClick: pick }, slots.default?.())
  },
})

function mountDialog() {
  setActivePinia(createPinia())
  return mount(FirmwareUploadDialog, {
    props: { modelValue: true },
    global: { stubs: { 'el-upload': ElUploadStub, ElUpload: ElUploadStub } },
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.upload.mockResolvedValue({ id: 1 })
})

describe('FirmwareUploadDialog', () => {
  it('表单不再要求手填版本号 / 目标型号', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    const text = wrapper.text()
    expect(text).not.toContain('版本号')
    expect(text).not.toContain('目标型号')
    // 但必须明确告诉用户要传哪两个文件
    expect(text).toContain('.meta.json')
    wrapper.unmount()
  })

  it('两个文件都没选时点上传 → 提示缺少镜像，且不发请求', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.find('[data-testid="firmware-upload-submit"]').trigger('click')
    await flushPromises()

    expect(mocks.upload).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="firmware-upload-error"]').text()).toContain('.bin')
    wrapper.unmount()
  })

  it('只选了 .bin 时点上传 → 提示与后端 FW_META_REQUIRED 同一句话，且不发请求', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    // 只点第一个上传框（accept=.bin）
    await wrapper.findAll('.upload-stub')[0].trigger('click')
    await wrapper.find('[data-testid="firmware-upload-submit"]').trigger('click')
    await flushPromises()

    expect(mocks.upload).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="firmware-upload-error"]').text()).toContain(FIRMWARE_ERROR_TEXT.FW_META_REQUIRED)
    wrapper.unmount()
  })

  it('两个文件齐备 → 上传体带 file + meta，成功后 emit uploaded 并关闭', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    const uploads = wrapper.findAll('.upload-stub')
    await uploads[0].trigger('click')
    await uploads[1].trigger('click')
    expect(wrapper.find('[data-testid="firmware-bin-file"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="firmware-meta-file"]').exists()).toBe(true)

    await wrapper.find('[data-testid="firmware-upload-submit"]').trigger('click')
    await flushPromises()

    expect(mocks.upload).toHaveBeenCalledTimes(1)
    const formData = mocks.upload.mock.calls[0][0] as FormData
    expect(formData.get('file')).toBeInstanceOf(File)
    expect(formData.get('meta')).toBeInstanceOf(File)
    expect(wrapper.emitted('uploaded')).toHaveLength(1)
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([false])
    wrapper.unmount()
  })

  it('服务端返回 FW_META_CONFLICT → 常驻错误里给出「用原始 sidecar 重传」的动作建议', async () => {
    mocks.upload.mockRejectedValue({ errorCode: 'FW_META_CONFLICT', message: 'meta conflict' })
    const wrapper = mountDialog()
    await flushPromises()
    const uploads = wrapper.findAll('.upload-stub')
    await uploads[0].trigger('click')
    await uploads[1].trigger('click')
    await wrapper.find('[data-testid="firmware-upload-submit"]').trigger('click')
    await flushPromises()

    const errorBox = wrapper.find('[data-testid="firmware-upload-error"]')
    expect(errorBox.exists()).toBe(true)
    expect(errorBox.text()).toContain('不一致')
    expect(wrapper.emitted('uploaded')).toBeUndefined()
    wrapper.unmount()
  })
})
