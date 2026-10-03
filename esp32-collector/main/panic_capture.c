/**
 * @file panic_capture.c
 * @brief 用 -Wl,--wrap=esp_panic_handler 注入崩溃捕获
 *
 * 为什么用 --wrap 而不是改 IDF 源码
 * --------------------------------
 * ESP-IDF 6.0 没有提供官方的 panic 钩子注册 API（grep ESP_PANIC / panic_hook
 * 在 components/esp_system 里无结果）。而 esp_panic_handler() 的调用点在
 * components/esp_system/port/panic_handler.c:258，是**跨编译单元**调用，因此
 * --wrap 可以可靠替换：__wrap_esp_panic_handler 先做捕获，再转发给
 * __real_esp_panic_handler，panic 原有行为（打印 + 复位）完全不变。
 *
 * 为什么捕获要放在转发之前
 * ----------------------
 * panic 之后 flash cache 可能被关闭；一旦 __real_esp_panic_handler 开始跑，
 * 取 uptime / 读栈就更不安全了。所以：
 *   1. 先用 esp_timer_get_time() 取 uptime（此刻 flash 通常仍可读）
 *   2. 调 crash_diag_capture_from_panic() 写 RTC SRAM
 *   3. 再交给 __real_esp_panic_handler() 走原有流程
 * 任一步失败都不影响第 3 步 —— 诊断绝不能改变 panic 本身的行为。
 */

#include <stdint.h>

#include "esp_attr.h"
#include "esp_timer.h"

#include "crash_diag.h"
#include "esp_private/panic_internal.h"

void __real_esp_panic_handler(panic_info_t *info);

/*
 * panic 上下文：不能调用 ESP_LOG*（会被静默重启吞掉，且可能触碰 flash）。
 * 这里只做 RTC 写入 + 定时器读取。
 */
void __wrap_esp_panic_handler(panic_info_t *info)
{
    int core = 0;
    int exception = (int)PANIC_EXCEPTION_FAULT;
    uintptr_t pc = 0;
    const void *frame = NULL;
    uint32_t uptime = 0;

    if (info != NULL) {
        core = info->core;
        exception = (int)info->exception;
        pc = (uintptr_t)info->addr;
        frame = info->frame;
    }

    /* 尽量取一次 uptime；失败就用 0（不让诊断失败影响 panic 流程） */
    uptime = (uint32_t)(esp_timer_get_time() / 1000000ULL);

    crash_diag_note_panic_uptime(uptime);
    crash_diag_capture_from_panic(core, exception, pc, frame);

    /* 走 IDF 原有 panic 流程（打印摘要 + 复位） */
    __real_esp_panic_handler(info);
}
