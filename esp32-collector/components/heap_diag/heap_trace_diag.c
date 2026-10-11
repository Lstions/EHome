/* heap_trace_diag.c —— task-34 的**归因工具**（诊断镜像专用，非交付配置）
 *
 * ## 为什么需要它（这轮最重要的结论）
 * 为了找"谁把 internal largest 从 31744 砍到 15360"，本卡连续**四次**靠
 * "算一个数 + 改一个候选"的方法，**四次全部打空**：
 *   1. 「link=n 23552 vs link=y 15360，差**恰好** 8192」⇒ 改任务栈 ⇒ largest 一字不变
 *   2. 「31744 − 16384 = 15360，且 free 侧自洽」⇒ 改 SSL_IN_CONTENT_LEN ⇒ 方向相反
 *   3. 改 SSL_OUT_CONTENT_LEN ⇒ 不变
 *   4. 开 MBEDTLS_DYNAMIC_BUFFER ⇒ 不变
 * 关键教训是第 2 条的量化版本：砍掉 8192 B 的 IN 上限，internal 只回收 **284 B**
 * ⇒ **"改一个候选可以完全不动堆"** ⇒ 枚举候选的信息量极低。
 *
 * ⇒ 改用**能把分配归因到调用栈**的工具：ESP-IDF 自带 heap trace。
 *   heap_trace_record_t 含 size + alloced_by[调用栈] ⇒ 一次运行就能**点名**，
 *   不再依赖任何"数值吻合"。
 *
 * ## 用法（诊断镜像）
 *   idf.py -B <dir> -DEHOME_MEM_DIAG=1 -DEHOME_HEAP_TRACE=1 reconfigure
 * 两个宏都开才生效；默认**完全不编译**，交付态零影响。
 *
 * ## 记录缓冲放哪
 *   STANDALONE 需要一块**静态**记录数组。放内部 RAM 会自己改变被测对象
 *   （本卡测的就是内部连续块）⇒ 优先放 PSRAM（EXT_RAM_BSS_ATTR）。
 *   若型号无 PSRAM 或未开 SPIRAM_ALLOW_BSS_SEG_EXTERNAL_MEMORY，则落回内部 .bss，
 *   并在运行时**如实报告**落点（不静默）。
 *
 * ⚠ 这是**诊断工具**：heap tracing 会让每次分配变慢，记录缓冲本身也占内存。
 *   不要把它编进交付镜像。
 */

#include "heap_trace_diag.h"

#include "sdkconfig.h"

#if defined(EHOME_MEM_DIAG) && defined(EHOME_HEAP_TRACE)

#include <string.h>

#include "esp_heap_trace.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_attr.h"

#define TAG "HEAPTRACE"

/* 记录条数：一次链路建立的全部内部分配通常在数百笔量级；2000 条留足余量。
 * 单条 ≈ 32 B（ccount 4 + address 4 + size 4 + freed 4 + 2×2 指针 ×4）⇒ 约 64 KB。
 * ⚠ 溢出会被 heap_trace_summary() 报出 has_overflowed，届时**加大再看**，
 *   不要靠"没看到就是没有"来下结论（那正是本卡反复踩的坑）。 */
#define HEAPTRACE_RECORDS 2000

/* 优先 PSRAM：本卡测的就是**内部**连续块，把工具放进内部会改变被测对象。
 * 无 PSRAM 型号自动落回内部，并由 heap_trace_diag_where() 如实报出。 */
#if defined(CONFIG_SPIRAM) && defined(CONFIG_SPIRAM_ALLOW_BSS_SEG_EXTERNAL_MEMORY)
#define HEAPTRACE_ATTR EXT_RAM_BSS_ATTR
#define HEAPTRACE_WHERE "PSRAM(.ext_ram.bss)"
#else
#define HEAPTRACE_ATTR
#define HEAPTRACE_WHERE "内部 .bss（本型号无 PSRAM 段或未开 ALLOW_BSS_SEG）"
#endif

static HEAPTRACE_ATTR heap_trace_record_t s_records[HEAPTRACE_RECORDS];
static bool s_inited;
static bool s_running;

const char *heap_trace_diag_where(void) { return HEAPTRACE_WHERE; }

bool heap_trace_diag_init(void)
{
    if (s_inited) return true;
    const esp_err_t err = heap_trace_init_standalone(s_records, HEAPTRACE_RECORDS);
    if (err != ESP_OK) {
        /* 不静默：装不上就必须说出来，否则会把"没采到"误读成"没有分配"。 */
        ESP_LOGE(TAG, "heap_trace_init_standalone 失败: %s", esp_err_to_name(err));
        return false;
    }
    s_inited = true;
    ESP_LOGW(TAG, "已装载：%d 条记录 @ %s（**诊断专用**，会拖慢分配）",
             (int)HEAPTRACE_RECORDS, HEAPTRACE_WHERE);
    return true;
}

void heap_trace_diag_start(const char *label)
{
    if (!heap_trace_diag_init()) return;
    /* 用 ALL 而不是 LEAKS：本卡关心的不是泄漏，而是"哪些分配**仍然存活**
     * 且**尺寸对得上**那个缺口"。ALL 才能看到完整的分配/释放过程；
     * LEAKS 会把已释放的记录移出缓冲区，反而丢掉"分配过又释放"的证据。 */
    const esp_err_t err = heap_trace_start(HEAP_TRACE_ALL);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "heap_trace_start 失败: %s", esp_err_to_name(err));
        return;
    }
    s_running = true;
    ESP_LOGW(TAG, ">>> 开始追踪: %s（口径 INTERNAL|8BIT，与内存门禁一致）",
             (label != NULL) ? label : "(未命名)");
}

void heap_trace_diag_stop_and_dump(const char *label)
{
    if (!s_running) return;
    const esp_err_t err = heap_trace_stop();
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "heap_trace_stop 失败: %s", esp_err_to_name(err));
        return;
    }
    s_running = false;

    heap_trace_summary_t sum;
    memset(&sum, 0, sizeof(sum));
    if (heap_trace_summary(&sum) == ESP_OK && sum.has_overflowed) {
        /* 溢出必须显式喊出来：否则"没看到那笔分配"会被误读成"它没发生"。 */
        ESP_LOGE(TAG, "记录缓冲**已溢出**（capacity=%u）⇒ 结果不完整，请加大条数重测",
                 (unsigned)sum.capacity);
    }

    ESP_LOGW(TAG, "<<< 结束追踪: %s  count=%u capacity=%u alloc=%u free=%u overflow=%u",
             (label != NULL) ? label : "(未命名)",
             (unsigned)sum.count, (unsigned)sum.capacity,
             (unsigned)sum.total_allocations, (unsigned)sum.total_frees,
             (unsigned)sum.has_overflowed);

    /* 只 dump 内部 RAM：本卡要定位的就是内部连续块，PSRAM 的几十条会淹掉输出。
     * ⚠ 若怀疑跨池耦合，再改 dump 全部。 */
    heap_trace_dump_caps(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT);
}

#else  /* 未开诊断 ⇒ 全部退化为空实现，编进交付态也无副作用 */

const char *heap_trace_diag_where(void) { return "(未编译)"; }
bool heap_trace_diag_init(void) { return false; }
void heap_trace_diag_start(const char *label) { (void)label; }
void heap_trace_diag_stop_and_dump(const char *label) { (void)label; }

#endif
