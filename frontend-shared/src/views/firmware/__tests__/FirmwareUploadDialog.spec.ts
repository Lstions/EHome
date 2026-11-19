import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h, ref } from 'vue'
import FirmwareUploadDialog from '../components/FirmwareUploadDialog.vue'
import { FIRMWARE_ERROR_TEXT } from '@/api/firmware'

/**
 * 上传对话框（单文件包，§6）。
 *
 * 必须成立的契约：
 *   ① 对话框里**只有**一个文件输入，accept 只收固件包（.ehfw / 同格式 .zip）；
 *   ② 选中裸 .bin → 当场报错且**不发请求**（文案必须指向 .ehfw）；
 *   ③ 上传体只有 `file` 一个字段（不再有 meta）；
 *   ④ `:limit="1"` 下重选（on-exceed）必须真的换掉文件（评审 A-8 的回归点）；
 *   ⑤ 服务端 `FW_PKG_*` 拒绝以**常驻**错误呈现，而不是一闪而过的 toast。
 */

const mocks = vi.hoisted(() => ({
  upload: vi.fn(),
  clearFiles: vi.fn(),
}))

vi.mock('@/api/firmware', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/firmware')>()
  return {
    ...actual,
    firmwareApi: { ...actual.firmwareApi, upload: mocks.upload },
  }
})

/** 固件包替身：名字/内容可指定 —— 「重选真的换了文件」要靠内容与名字一起判。 */
function packageFile(name = 'ehome_collector-2.9.0-c6-n8.ehfw', body = 'PK\x03\x04zip') {
  return new File([body], name, { type: 'application/zip' })
}

/** 裸镜像替身：改造前它是必传的一半，现在是最常见的一种「拿错文件」。 */
function rawBinFile() {
  return new File(['\xE9bin'], 'ehome_collector.bin', { type: 'application/octet-stream' })
}

/**
 * el-upload 的替身：复刻真实组件的两个钩子语义 ——
 * `:limit` 已满时**只**触发 on-exceed，不触发 on-change（正是 A-8 的成因）。
 * happy-dom 点不动真实 `<input type=file>`，所以由用例直接调 `pick(file)` 交文件。
 */
const ElUploadStub = defineComponent({
  name: 'ElUploadStub',
  props: {
    autoUpload: Boolean,
    limit: Number,
    accept: String,
    showFileList: Boolean,
    onChange: Function,
    onExceed: Function,
  },
  setup(props, { slots, expose }) {
    const files = ref<unknown[]>([])
    const clearFiles = () => {
      mocks.clearFiles()
      files.value = []
    }
    const pick = (file: File) => {
      const uploadFile = { name: file.name, raw: file, size: file.size, status: 'ready' }
      if (props.limit && files.value.length + 1 > props.limit) {
        props.onExceed?.([file], files.value)
        return
      }
      files.value.push(uploadFile)
      props.onChange?.(uploadFile, files.value)
    }
    expose({ clearFiles, pick })
    return () => h('div', { class: 'upload-stub' }, slots.default?.())
  },
})

function mountDialog() {
  setActivePinia(createPinia())
  return mount(FirmwareUploadDialog, {
    props: { modelValue: true },
    global: { stubs: { 'el-upload': ElUploadStub, ElUpload: ElUploadStub } },
  })
}

function uploadStubs(wrapper: ReturnType<typeof mountDialog>) {
  return wrapper.findAllComponents({ name: 'ElUploadStub' })
}

/** 交一个文件给唯一的那个 el-upload（走它自己的 limit 语义）。 */
async function pick(wrapper: ReturnType<typeof mountDialog>, file: File) {
  const stub = uploadStubs(wrapper)[0]
  ;(stub.vm as unknown as { pick: (f: File) => void }).pick(file)
  await flushPromises()
}

function submit(wrapper: ReturnType<typeof mountDialog>) {
  return wrapper.find('[data-testid="firmware-upload-submit"]')
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.upload.mockResolvedValue({ id: 1 })
})

describe('FirmwareUploadDialog · 单文件包', () => {
  it('只有一个文件输入，accept 只收 .ehfw / .zip', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    const stubs = uploadStubs(wrapper)
    expect(stubs).toHaveLength(1)
    expect(stubs[0].props('accept')).toBe('.ehfw,.zip')
    // 界面上必须说清楚要传的是包，而不是「.bin + .meta.json 两件」
    expect(wrapper.text()).toContain('.ehfw')
    expect(wrapper.text()).not.toContain('.meta.json')
    wrapper.unmount()
  })

  it('表单不再要求手填版本号 / 目标型号', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    const text = wrapper.text()
    expect(text).not.toContain('版本号')
    expect(text).not.toContain('目标型号')
    wrapper.unmount()
  })

  it('选中裸 .bin → 立即报「这是裸镜像」，且不发请求', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await pick(wrapper, rawBinFile())

    expect(mocks.upload).not.toHaveBeenCalled()
    const errorBox = wrapper.find('[data-testid="firmware-upload-error"]')
    expect(errorBox.exists()).toBe(true)
    expect(errorBox.text()).toContain('裸镜像')
    expect(errorBox.text()).toContain('.ehfw')
    // selectedInvalid（§6.2）：没有合法选择时上传是 disabled 的
    expect(submit(wrapper).attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('选中 .meta.json（已废弃的第二个文件）→ 报「不是固件包」', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await pick(wrapper, new File(['{"profile":"c6-n8"}'], 'ehome_collector.meta.json', { type: 'application/json' }))

    expect(wrapper.find('[data-testid="firmware-upload-error"]').text()).toContain('不是固件包')
    expect(mocks.upload).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('一个文件都没选时上传按钮禁用，点了也不发请求', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    expect(submit(wrapper).attributes('disabled')).toBeDefined()
    await submit(wrapper).trigger('click')
    await flushPromises()
    expect(mocks.upload).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('选中 .ehfw → 上传体只有 file 一个字段，成功后 emit uploaded 并关闭', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await pick(wrapper, packageFile())
    expect(wrapper.find('[data-testid="firmware-package-file"]').text()).toContain('.ehfw')

    await submit(wrapper).trigger('click')
    await flushPromises()

    expect(mocks.upload).toHaveBeenCalledTimes(1)
    const formData = mocks.upload.mock.calls[0][0] as FormData
    expect([...formData.keys()]).toEqual(['file'])
    expect((formData.get('file') as File).name).toBe('ehome_collector-2.9.0-c6-n8.ehfw')
    expect(formData.get('meta')).toBeNull()
    expect(wrapper.emitted('uploaded')).toHaveLength(1)
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([false])
    wrapper.unmount()
  })

  it('重选必须生效（评审 A-8）：第二次选择只触发 on-exceed，仍能换掉文件', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    const second = packageFile('second.ehfw', 'PK\x03\x04second')
    await pick(wrapper, packageFile('first.ehfw', 'PK\x03\x04first'))
    expect(wrapper.find('[data-testid="firmware-package-file"]').text()).toContain('first.ehfw')

    // 第二次选择：limit=1 已满 → 替身只调 on-exceed（真实 el-upload 同样如此）
    await pick(wrapper, second)

    // on-exceed 里必须 clearFiles 后手动接管，否则文件仍是 first.ehfw
    expect(mocks.clearFiles).toHaveBeenCalled()
    expect(wrapper.find('[data-testid="firmware-package-file"]').text()).toContain('second.ehfw')

    await submit(wrapper).trigger('click')
    await flushPromises()
    const formData = mocks.upload.mock.calls[0][0] as FormData
    // happy-dom 的 FormData.get 返回的是副本（不是追加时的同一实例），故按名字+字节数判定
    const uploaded = formData.get('file') as File
    expect(uploaded.name).toBe('second.ehfw')
    expect(uploaded.size).toBe(second.size)
    wrapper.unmount()
  })

  it('重选成裸 .bin → 上一个包被清掉，上传重新禁用并报错', async () => {
    const wrapper = mountDialog()
    await flushPromises()

    await pick(wrapper, packageFile())
    await pick(wrapper, rawBinFile())

    expect(wrapper.find('[data-testid="firmware-package-file"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="firmware-upload-error"]').text()).toContain('裸镜像')
    expect(submit(wrapper).attributes('disabled')).toBeDefined()
    await submit(wrapper).trigger('click')
    await flushPromises()
    expect(mocks.upload).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('服务端返回 FW_PKG_CONFLICT → 常驻错误给出「重新打包重传」的动作建议', async () => {
    mocks.upload.mockRejectedValue({ errorCode: 'FW_PKG_CONFLICT', message: 'pkg conflict' })
    const wrapper = mountDialog()
    await flushPromises()
    await pick(wrapper, packageFile())
    await submit(wrapper).trigger('click')
    await flushPromises()

    const errorBox = wrapper.find('[data-testid="firmware-upload-error"]')
    expect(errorBox.exists()).toBe(true)
    expect(errorBox.text()).toContain(FIRMWARE_ERROR_TEXT.FW_PKG_CONFLICT)
    expect(wrapper.emitted('uploaded')).toBeUndefined()
    wrapper.unmount()
  })
})
