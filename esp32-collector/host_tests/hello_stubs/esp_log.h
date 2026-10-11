#ifndef HELLO_HOST_ESP_LOG_H
#define HELLO_HOST_ESP_LOG_H
/* 注意：这些宏【引用 tag】而不是写成 ((void)0)。
 * 原因：真实 IDF 的宏会用到 tag，于是 if 里的 `static const char *TAG` 是被使用的；
 * 若 stub 完全丢弃 tag，某些源文件会在 -Werror=unused-variable 下【因为 stub 而报错】,
 * 那是"脚手架造成的假问题"（本项目反复踩过）。((void)(tag)) 保留这一语义。 */
#define ESP_LOGE(tag, format, ...) ((void)(tag))
#define ESP_LOGW(tag, format, ...) ((void)(tag))
#define ESP_LOGI(tag, format, ...) ((void)(tag))
/* 2026-10-06 新增：transport.c 用到 ESP_LOGD（加法，不影响既有用例） */
#define ESP_LOGD(tag, format, ...) ((void)(tag))
#define ESP_LOGV(tag, format, ...) ((void)(tag))
#endif
