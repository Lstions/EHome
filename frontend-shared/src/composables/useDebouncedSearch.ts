import { ref, computed, watch, type Ref } from 'vue'

/**
 * 搜索输入 debounce composable
 * 用于列表页搜索框，减少不必要的 filter 计算
 */
export function useDebouncedSearch<T extends { name?: string; device_type?: string }>(
  items: Ref<T[]>,
  options: {
    delay?: number
    searchFields?: (item: T) => string[]
  } = {}
) {
  const { delay = 300, searchFields = (item) => [item.name || '', item.device_type || ''] } = options

  const searchKeyword = ref('')
  const debouncedKeyword = ref('')
  let debounceTimer: ReturnType<typeof setTimeout> | null = null

  // 立即执行（如清除时）
  const flush = () => {
    if (debounceTimer) {
      clearTimeout(debounceTimer)
      debounceTimer = null
    }
    debouncedKeyword.value = searchKeyword.value
  }

  watch(searchKeyword, (val) => {
    if (debounceTimer) clearTimeout(debounceTimer)
    if (!val) {
      // 清空时立即更新
      debouncedKeyword.value = ''
      return
    }
    debounceTimer = setTimeout(() => {
      debouncedKeyword.value = val
    }, delay)
  })

  const filteredItems = computed(() => {
    const kw = debouncedKeyword.value.trim().toLowerCase()
    const list = items.value || []
    if (!kw) return list
    return list.filter(item =>
      searchFields(item).some(field => (field || '').toLowerCase().includes(kw))
    )
  })

  const clear = () => {
    searchKeyword.value = ''
    flush()
  }

  return {
    searchKeyword,
    debouncedKeyword,
    filteredItems,
    clear,
    flush,
  }
}
