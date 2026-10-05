/*
 * hello_caps_stub.c — V3-2a 宿主测试用的默认能力位桩。
 *
 * 返回 0（"服务端未声明任何能力"），这正是 V3-2a 之前固件的等效状态：
 * bus_worker 的 DataBatch 路径被短路，report_tx 只发 0x03。任何直接编入
 * bus_worker.c 的既有用例（bus_worker_report_tests / bus_worker_rx_tests /
 * bus_worker_batch_tests / rx_health_e2e_tests）因此继续验证现状路径。
 *
 * 需要验证"能力位置位后会发 0x20"的用例
 * （bus_worker_data_batch_tests.c）**不**链接本文件，而是自己定义
 * hello_get_server_caps() 并注入可控能力位。
 */
#include <stdint.h>

uint64_t hello_get_server_caps(void)
{
    return 0;
}
