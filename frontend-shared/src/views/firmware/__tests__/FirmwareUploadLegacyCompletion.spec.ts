import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h } from 'vue'
import FirmwareUploadDialog from '../components/FirmwareUploadDialog.vue'
import FirmwareManage from '../FirmwareManage.vue'
import { firmwareUploadOutcome, type Firmware } from '@/api/firmware'

/**
 * 上传的 legacy 补齐语义（契约补充：命中同 elf_sha256 的存量行 → 200 + completed_legacy=true）。
 *
 * 为什么必须有这一组：补齐与「新增」「重复」在界面上是**三件不同的事**。
 * 说错任何一种，运维都会做出错误动作 ——
 *   · 说成「上传成功」→ 以为库里多了一条，去核对条数；
 *   · 说成「重复/失败」→ 以为这次没生效，反复重传（正是 DEF-10 的形态）。
 * 另外补齐路径**不新增记录**，所以列表刷新必须成立，否则用户看不到 psram / 身份状态的变化。
 */

const mocks = vi.hoisted(() => ({
  upload: vi.fn(),
  getList: vi.fn(),
  success: vi.fn(),
  info: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))

vi.mock('@/api/firmware', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/firmware')>()
  return {
    ...actual,
    firmwareApi: {
      ...actual.firmwareApi,
      upload: mocks.upload,
      getList: mocks.getList,
    },
  }
})

vi.mock('@/utils/feedback', () => {
  const feedback = {
    success: mocks.success,
    info: mocks.info,
    error: mocks.error,
    warning: mocks.warning,
    handleError: mocks.error,
    handleErrorWithContext: mocks.error,
    confirmDanger: vi.fn(() => Promise.resolve(true)),
    confirm: vi.fn(() => Promise.resolve(true)),
  }
  return { feedback, default: feedback }
})

/** el-upload 替身：按 accept 交出对应文件（与既有上传用例同一手法）。 */
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

function legacyRow(): Firmware {
  return {
    id: 7,
    version: '2.9.0',
    filename: 'ehome_collector-2.9.0.bin',
    checksum: 'a'.repeat(64),
    size_bytes: 1024,
    url: '/api/v1/firmwares/7/download',
    created_at: '2026-11-18T00:00:00Z',
    chip: '',
    flash_mb: 0,
    psram: '',
    model_key: '',
    build_profile: '',
    channel: 'internal',
    identity_state: 'legacy',
  }
}

function completedRow(): Firmware {
  return {
    ...legacyRow(),
    chip: 'esp32c6',
    flash_mb: 8,
    psram: 'no',
    model_key: 'esp32c6-n8',
    build_profile: 'c6-n8',
    identity_state: 'verified',
  }
}

function mountDialog() {
  setActivePinia(createPinia())
  return mount(FirmwareUploadDialog, {
    props: { modelValue: true },
    global: { stubs: { 'el-upload': ElUploadStub, ElUpload: ElUploadStub } },
  })
}

async function pickBothFilesAndUpload(wrapper: ReturnType<typeof mountDialog>) {
  const uploads = wrapper.findAll('.upload-stub')
  await uploads[0].trigger('click')
  await uploads[1].trigger('click')
  await wrapper.find('[data-testid="firmware-upload-submit"]').trigger('click')
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.upload.mockResolvedValue({ ...completedRow(), completed_legacy: true })
})

describe('firmwareUploadOutcome（文案真源）', () => {
  it('completed_legacy=true → info + 「补齐」，且明确「未新增记录」', () => {
    const outcome = firmwareUploadOutcome({ completed_legacy: true })
    expect(outcome.level).toBe('info')
    expect(outcome.message).toContain('补齐')
    expect(outcome.message).toContain('未新增记录')
  })

  it('completed_legacy 缺省 / false → success + 「上传成功」（向后兼容旧后端）', () => {
    expect(firmwareUploadOutcome({ completed_legacy: false })).toEqual({ level: 'success', message: '上传成功' })
    expect(firmwareUploadOutcome({})).toEqual({ level: 'success', message: '上传成功' })
    expect(firmwareUploadOutcome(null)).toEqual({ level: 'success', message: '上传成功' })
    expect(firmwareUploadOutcome(undefined)).toEqual({ level: 'success', message: '上传成功' })
  })

  it('只有严格 true 才算补齐（字符串 "true" 不得误报）', () => {
    const outcome = firmwareUploadOutcome({ completed_legacy: 'true' as unknown as boolean })
    expect(outcome.level).toBe('success')
  })
})

describe('FirmwareUploadDialog · legacy 补齐提示', () => {
  it('completed_legacy=true → feedback.info（不是 success），并照常 emit uploaded', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await pickBothFilesAndUpload(wrapper)

    expect(mocks.info).toHaveBeenCalledWith('已补齐该固件的身份信息（未新增记录）')
    expect(mocks.success).not.toHaveBeenCalled()
    // 补齐路径同样要触发刷新：否则列表仍显示旧的 psram / 身份状态
    expect(wrapper.emitted('uploaded')).toHaveLength(1)
    wrapper.unmount()
  })

  it('completed_legacy=false → 保持 success「上传成功」（既有行为不回归）', async () => {
    mocks.upload.mockResolvedValue({ ...completedRow(), completed_legacy: false })
    const wrapper = mountDialog()
    await flushPromises()
    await pickBothFilesAndUpload(wrapper)

    expect(mocks.success).toHaveBeenCalledWith('上传成功')
    expect(mocks.info).not.toHaveBeenCalled()
    expect(wrapper.emitted('uploaded')).toHaveLength(1)
    wrapper.unmount()
  })

  it('字段缺省（旧后端）→ 仍走 success「上传成功」', async () => {
    mocks.upload.mockResolvedValue(completedRow())
    const wrapper = mountDialog()
    await flushPromises()
    await pickBothFilesAndUpload(wrapper)

    expect(mocks.success).toHaveBeenCalledWith('上传成功')
    expect(mocks.info).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('补齐路径的列表刷新（同一 id 被更新，不是新增）', () => {
  it('uploaded 之后强制重新拉列表，列表呈现补齐后的身份字段', async () => {
    setActivePinia(createPinia())
    mocks.getList.mockResolvedValueOnce({ total: 1, list: [legacyRow()] })
    mocks.getList.mockResolvedValueOnce({ total: 1, list: [completedRow()] })

    const wrapper = mount(FirmwareManage, {
      global: {
        stubs: {
          PageHeader: { template: '<div class="page-header"><slot name="extra" /></div>' },
        },
      },
    })
    await flushPromises()

    // 补齐前：列表里是 legacy 行（psram 空 / 身份 legacy）
    expect(mocks.getList).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('legacy')

    // 上传对话框补完身份 → 冒泡 uploaded → 页面重新拉列表（force，绕过 60s 缓存）
    const dialog = wrapper.findComponent(FirmwareUploadDialog)
    dialog.vm.$emit('uploaded')
    await flushPromises()

    expect(mocks.getList).toHaveBeenCalledTimes(2)
    const text = wrapper.text()
    expect(text).toContain('verified')
    expect(text).not.toContain('legacy')
    // 仍是同一条记录（id 未变），不是「多了一条」
    expect(text).toContain('esp32c6-n8')
    wrapper.unmount()
  })
})
