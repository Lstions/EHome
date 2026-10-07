/**
 * @file device_link_wiring.h
 * @brief 把 3.0 设备侧链路（TCP + mTLS）接进 main —— "固件里根本没有 3.0 链路"的直接修法
 *
 * ## 这一层接什么
 *
 *     tls_esp(mTLS I/O) ─注入 io─> session ─内含─> link_tcp / link_rx_adapt / rx_pump / wire
 *
 * `session` 自己就把 `link_tcp` / `link_rx_adapt` / `rx_pump` / `wire` 串起来了
 * （见 session.c 里 `rx_pump_create(...)`），所以 main 只需创建 tls_esp 的 I/O 配置
 * 与 `session`，整条 3.0 接收链就都在固件里了。
 *
 * ## 决策与胶水分开（本仓的既定做法，见 hello_handshake_*.c）
 *
 * 真正**有判断**的三件事被抽成纯函数，可在宿主上测（`host_tests/device_link_wiring_tests.c`）：
 *   1. `device_link_delim_bytes` —— 定界器要一次性分配多少**连续**字节；
 *   2. `device_link_check_placement` —— 这个连续块在本型号上放得下吗（P8：差异只影响放置）；
 *   3. `devlink_cert_check` —— 单份证书材料的判定（空 / 超上限 / 可用）。
 * 剩下的只是**胶水**（读 NVS、建任务、装配置），不产生判断。
 *
 * ## 为什么不是 `#ifdef` 掉整段实现
 *
 * 实现不带 `#ifdef` 包整段（只在函数里判 `CONFIG_EHOME_DEVICE_LINK_ENABLED`），
 * 目的是让"接线"在**源码层是真实的**：main 里有一个真实调用点，编译器确实编译它，
 * 而不是让整个文件在预处理期消失。
 *
 * ### 开关 n 时到底发生什么（我在这里错了**两次**，最终结论经反汇编验证）
 *
 * **第一次错**：写过"代码始终编译 ⇒ 门禁的『可达』与『真的被调用』是同一件事"。
 *   编译得过 ≠ 进了镜像，这句已删。
 *
 * **第二次错（更隐蔽）**：改成"开关 n 时整个分支被常量折叠成死代码、被 `--gc-sections`
 *   丢弃，ELF 里符号数为 0"。**这也是错的**，两个独立证据（2026-10-07，s3-n16）：
 *   1. 默认构建（`# CONFIG_EHOME_DEVICE_LINK_ENABLED is not set`）的 ELF 里
 *      `session_create / session_poll / tls_esp_io / tls_esp_connect / rx_pump_create /
 *      wire_delim_create / link_tcp_read / link_rx_adapt_read / sntp_mgr_create …`
 *      **11/11 全部存在**；`device_link_wiring_init` = `T 4201599c size 0x29f`。
 *   2. 反汇编该函数，内部**真的调用** `device_link_check_placement` 与 `variant_caps`。
 *
 *   根因（我为什么会测出"0"）：`devlink_wanted()` 是**运行期函数**而非编译期常量，
 *   分支不会被消除。而我当时跑 `xtensa-esp32s3-elf-nm` **没有 source export.sh**
 *   ⇒ 命令不存在 ⇒ 我把 stderr 用 `2>/dev/null` 吞掉 ⇒ `grep -c` 打印 `0`
 *   ⇒ 我把"命令根本没跑成"读成了"符号不存在"。
 *   **又一次"命令跑通 ≠ 测到了东西"。**
 *
 * **正确表述（已由符号表 + 反汇编验证）**：
 *   - 开关 n 时**代码在镜像里**，保护机制是 `device_link_wiring_init` 开头的
 *     **运行期早退**（`if (!devlink_wanted()) { log; return; }`，先于一切副作用）；
 *   - ⇒ **可观测行为不变**（不建任务、不分配堆、不发一个包）—— 这是验收口径；
 *   - ⇒ **但镜像不是逐字节不变**：s3-n16 `.bin` 约 +3.2 KB（代码/字符串/对齐）。
 *     若要按"默认产物完全不变"验收，这条**不成立**，请按"**行为**不变"验收。
 *
 * ## ⚠ 前置条件清单（**status reviewed 2026-10-07** —— 请连同状态一起读）
 *
 * 1. **SNTP** —— **已落地**。
 *    `now_epoch` 传的是本文件所在模块 :502 的 `devlink_now_epoch()`（**不是 NULL**），
 *    它读 `sntp_mgr_now()`；取不到时返回 0 ⇒ 时间不可信 ⇒ `tls_guard` 把证书类
 *    失败判为**可自愈**（soft，退避重试）而不是致命。这正是 §52.2 的分级意图。
 *    ⚠ 这一段此前写的是"目前传 NULL / SNTP 尚未落地" —— 写的时候是真的，
 *    但**过期**了。**过期清单比没有清单更糟**：它让人以为剩下的工作就是这些。
 *
 * 2. **证书铺开** —— **尚未实现**（这条仍然成立）。
 *    设计指定"证书/私钥入 **NVS 加密分区**"，该分区**尚未实现**
 *    （分区表里没有 `nvs_keys`）。读不到材料时**不假装成功**，而是照常构造
 *    → `tls_guard` 判 `hard_fatal` ⇒ `SESSION_FATAL`（**不重试**，避免把
 *    "没铺开"掩盖成"网络抖动"而无限重试）。
 *    ⚠ 必须明说的后果：今天写进去的证书**与私钥**是 **flash 里的明文**。
 *
 * 3. **上行成帧** —— **已修**（§134 / task-31、task-32）。
 *    这一条当初**不在这张清单里**，而它是三条里最致命的：
 *    前两条都满足时链路**仍然握不上手**（生产上行缺 12 B 帧头 ⇒ 后端 ErrMagic）。
 *    ⇒ 清单只有**完整**才有用。
 *
 * ## 纪律（写在这里以免重犯）
 * 关掉上述任一条时**必须同一次提交里更新这张表**；仓库另有
 * `tools/check_baseline_sync.py` 在守文档漂移。
 *
 * ⇒ 开启开关**不等于**能用；它等于"把剩下的缺口变成可见、且带明确处置的状态"。
 */
#ifndef EHOME_DEVICE_LINK_WIRING_H
#define EHOME_DEVICE_LINK_WIRING_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "variant.h"   /* variant.h 不依赖 IDF，宿主可编译 */

#ifdef __cplusplus
extern "C" {
#endif

/* ── 纯判定（宿主可测）── */

/** 单份证书材料的判定。 */
typedef enum {
    DEVLINK_CERT_OK = 0,     /* 有材料且在容量上限内 */
    DEVLINK_CERT_EMPTY,      /* 长度 0：没铺开 */
    DEVLINK_CERT_TOO_BIG,    /* 超上限：**不放宽上限**，按不可用处理 */
} devlink_cert_t;

/** 放置判定：这条链路在本型号上起得来吗。 */
typedef enum {
    DEVLINK_PLACE_OK = 0,
    DEVLINK_PLACE_NO_VARIANT,          /* 取不到型号能力：**不猜**，拒绝 */
    DEVLINK_PLACE_EXCEEDS_CONTIGUOUS,  /* 单笔连续块（定界器）超型号上界 */
    /* ⭐ **并存**判据：定界器与 TLS 记录缓冲**同时存活**，两者的连续需求之和超上界。
     * 与上一条分成两个取值，是因为**两个上界数字完全不同**
     * （S3/S3P：单笔上限 23536，并存上限 7152）——
     * 合成一个取值会让操作员不知道该调小到多少。见 .c 里的详细说明。 */
    DEVLINK_PLACE_EXCEEDS_COEXIST,
} devlink_place_t;

const char *devlink_cert_name(devlink_cert_t v);
const char *devlink_place_name(devlink_place_t v);

/** 单份证书判定。cap 是容量上限。 */
devlink_cert_t devlink_cert_check(size_t blob_bytes, size_t cap);

/* ── PEM 终止符（2026-10-07，真机抓到 MBEDTLS_ERR_X509_INVALID_FORMAT 后补）──
 *
 * esp-tls 的契约（components/esp-tls/esp_tls.h:111-112, 137-140）：
 *   PEM 缓冲**必须以 NUL 终止**，且 *_bytes **含**这个终止符。
 * 而 NVS 里存的是文件原样字节（PEM 末尾是 0x0A，没有 NUL）
 * ⇒ 直接传 blob 与 blob 长度会让 mbedtls 解析失败。
 *
 * ⚠ 这两个函数存在的意义：把这条契约变成**一处具名定义**并有宿主测试钉住，
 * 而不是让每个调用点各自"记得 +1"。真机上就是漏了这一步。
 */

/** NVS blob 转成 PEM 后应有的缓冲字节数（含终止符）。 */
size_t devlink_pem_buf_bytes(size_t raw_len);

/**
 * 在 buf 的 raw_len 处写入 NUL，并把长度（**含终止符**）写进 out_len。
 *
 * @param buf     至少 devlink_pem_buf_bytes(raw_len) 字节可写
 * @param cap     buf 容量
 * @param raw_len NVS blob 的原始字节数（不含终止符）
 * @param out_len 输出：**含终止符**的长度，可直接用于 esp-tls 的 *_bytes
 * @return true 成功；false = 参数为空或容量不足（此时**一个字节都不写**）
 */
bool devlink_pem_terminate(uint8_t *buf, size_t cap, size_t raw_len, size_t *out_len);
/**
 * 定界器一次性分配的**连续**字节数 = max_payload + 12(头) + 4(CRC)。
 * 上界常量取自 wire.h（P5：唯一来源，不在这里重抄一份）。
 */
uint32_t device_link_delim_bytes(uint32_t max_payload);

/**
 * 这条链路的**连续块需求**在本型号上放得下吗。
 * 用型号的 `internal_contiguous_max`（largest 类判据）而不是 free ——
 * OTA 那次"free 够、largest 不够"的事故就是这一类。
 *
 * ⚠ **两条判据，不是一条**（2026-10-07 补，详见 .c 里的推导）：
 *   1. `delim = max_payload + 16` 单笔 ≤ 上界；
 *   2. `delim + tls_in_bytes` ≤ 上界 —— 因为定界器缓冲与 mbedTLS 记录缓冲
 *      **在会话存活期内同时存在**，两笔都要各自找到连续内部块。
 *
 * `tls_in_bytes` 传 0 表示"不评估并存约束"（只做单笔判据，等价于旧行为）。
 * ⇒ 调用方应传**真实的** `CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN`；
 *    传 0 只在"该构建没有 TLS"时才正确。
 */
devlink_place_t device_link_check_placement(uint32_t max_payload,
                                            uint32_t tls_in_bytes,
                                            const variant_caps_t *caps);

/* ── SNTP 接线里的判定（宿主可测）── */

/**
 * 网络可用性的**边沿**。
 *
 * 为什么要把这件事显式抽出来而不是写 `if (net != prev)`：
 * sntp_mgr_network_down() 会把状态压回 IDLE，**每轮都通知 down** 会让状态机
 * 永远停在"不能发起"的那一步 —— 而这么写出来的代码看起来正在认真接线，
 * 也没有任何一处会报错（正是本仓反复出现的"建好了但没插电"）。
 * 抽成纯函数后，"只在边沿通知"这条契约可以在宿主上被锁住。
 */
typedef enum {
    DEVLINK_NET_EDGE_NONE = 0,   /* 无变化：**不要**重复通知 */
    DEVLINK_NET_EDGE_UP,         /* 不可用 -> 可用 */
    DEVLINK_NET_EDGE_DOWN,       /* 可用 -> 不可用 */
} devlink_net_edge_t;

devlink_net_edge_t devlink_net_edge(bool was_up, bool is_up);

/**
 * `tls_esp_config_t.now_epoch` 的取值规则。
 *
 * 取不到时间一律给 **0**（由 tls_guard 判为不可信），**绝不猜**一个
 * "看起来合理"的值：伪造一个 2026 年的时间会让证书校验**看起来**通过，
 * 把"根本没校时"变成一次无法归因的失败。
 *
 * ⚠ 刻意**不**在这里做区间钳制：TLS_GUARD_MIN/MAX_EPOCH 是 tls_guard 的
 * 单一来源（P4）。适配器里再判一次，阈值就有了两个答案；而且"被适配器
 * 钳过的时间"会让 tls_guard 的判定失去意义（它才是唯一裁判）。
 */
uint64_t devlink_now_epoch_value(bool have_epoch, uint64_t epoch);

/* ── 3.0 下行帧的分发判定（宿主可测）──
 *
 * ## 为什么要把这件事抽出来
 * devlink_on_msg 原先**只打日志 + 记 type**，从不调用 msg_handler_process
 * ⇒ 后端经 3.0 链路下发的 0x22 远程运维（重启/恢复出厂）、配置下发等
 * **被静默丢弃**：操作员点"重启"，设备毫无反应且**没有任何错误**。
 * 而"补上调用"还不够 —— 还必须有 §118.4 登记的**类型一致性校验**：
 * 头里的 type 与 payload 首字节的 type 是同一语义的两处表示，
 * 不一致时若"挑一个信"，就会出现「按 header 派发、按 payload 解码」
 * 的**静默错派发**（派给了 A，解码出的却是 B 的字段）。
 *
 * 这两件事（要不要丢弃、丢弃的原因是什么）都是纯判定，因此放在宿主可测区：
 * 三个失败面（空 payload / 类型不一致 / 正常帧被误伤）可以在宿主机上穷举，
 * 而不是只能靠"真机上点一下重启试试"。
 */

/** 一条下行帧的处置结论。每个取值对应调用方**一个**明确分支（P1）。 */
typedef enum {
    /** 类型一致 ⇒ 交给分发。 */
    DEVLINK_RX_DISPATCH = 0,
    /** payload 为空（或指针为空）⇒ 丢弃。**不读 payload[0]**（越界）。 */
    DEVLINK_RX_DROP_EMPTY,
    /** payload[0] != header.type ⇒ 丢弃 + 计数。**不挑一个信**。 */
    DEVLINK_RX_DROP_TYPE_MISMATCH,
} devlink_rx_verdict_t;

const char *devlink_rx_verdict_name(devlink_rx_verdict_t v);

/**
 * 纯判定：这条下行帧该怎么处理。
 *
 * @param header_type 帧头里的 type（rx_msg_t.type）
 * @param payload_len 载荷长度（rx_msg_t.payload_len）
 * @param payload     载荷起始（**含首字节**，rx_msg_t.payload）
 *
 * ⚠ 只有 payload_len >= 1 时才读 payload[0]。
 * ⚠ 语义注意：本函数判的是"**该不该发**"，不是"payload 里的业务布局"。
 *   它只碰 payload 的**第 0 字节**（2.x 的类型约定），其余一个字节都不解释
 *   —— 3.0 的 payload 业务布局仍是设计待确认项，这里不猜。
 */
devlink_rx_verdict_t devlink_rx_verdict(uint8_t header_type, uint16_t payload_len,
                                        const uint8_t *payload);

/** 3.0 下行路径的计数（诊断用）。 */
typedef struct {
    uint32_t dispatched;             /* 真正交给分发的帧数 */
    uint32_t dropped_empty;          /* payload 为空而丢弃 */
    uint32_t dropped_type_mismatch;  /* 头/载荷类型不一致而丢弃 */
} devlink_rx_stats_t;

/** 分发入口（默认是 msg_handler_process）。做成函数指针是为了宿主可测：
 *  测试注入一个假的，直接观察"到底有没有进分发、进去的是哪几个字节"。 */
typedef void (*devlink_dispatch_fn)(const uint8_t *data, size_t len);

/**
 * 判定 + 分发（把判定应用到 dispatch，并累计 stats）。
 *
 * @param dispatch 分发入口；为 NULL 时**只判定不分发**（仍计数）
 * @param stats    计数累加目标；可为 NULL
 * @return 实际采取的动作
 *
 * ⚠ 只在 DEVLINK_RX_DISPATCH 时才调用 dispatch，且传的是 **payload 原样**
 *   （含首字节）—— msg_handler_process 是按 data[0] 取类型的（2.x 约定），
 *   传 header 会让它读到错误的消息号。
 */
devlink_rx_verdict_t devlink_rx_handle(uint8_t header_type, uint16_t payload_len,
                                       const uint8_t *payload,
                                       devlink_dispatch_fn dispatch,
                                       devlink_rx_stats_t *stats);

/* ── 胶水 ── */

/**
 * 初始化 3.0 设备侧链路。幂等（重复调用只有第一次生效）。
 * 未启用时**不产生任何副作用**，只打一行说明后返回。
 * 自己等 WiFi 就绪，不要求调用方保证网络已在。
 */
void device_link_wiring_init(void);

/** 当前会话状态名（未启用/未创建时返回 "DISABLED" / "NONE"）；诊断用。 */
const char *device_link_wiring_state_name(void);

/**
 * task-21：**只读**会话访问器 —— 供上行仲裁层查询 session_state。
 *
 * 为什么需要它：`s_session` 是本文件的 static（device_link_wiring.c:420），
 * 而仲裁层要读 `session_state(sess)` 才能回答"3.0 现在能不能承载上行"。
 *
 * ⚠ 为什么返回 `session_t *` 而不是把 `s_session` 暴露成全局：
 * 全局变量任何人都能**写**（例如误调 session_destroy 后留下悬空指针）；
 * 访问器只给读句柄，**所有权仍在本文件**。调用方**不得** destroy 它。
 *
 * @return 会话句柄；未启用（Kconfig=n）或尚未创建时返回 **NULL**。
 *
 * ⚠ 用 `struct session *` 前置声明而不是 include "session.h"：
 * 本头会被 `host_tests/device_link_wiring_tests.c` 包含，而那个 target 的
 * include 路径里**没有** `components/session/include`（见 host_tests/CMakeLists.txt:1144-1148）
 * ⇒ include 会让该 target 编译失败。而 `session_t` 本身就是
 * `typedef struct session session_t;`（session.h:77）⇒ 前置声明**类型完全等价**。
 */
struct session;
struct session *device_link_wiring_session(void);

/* ── ⭐ 3.0 上行**成帧**（task-31 修：生产上行此前完全不成帧）──
 *
 * ## 缺陷是什么（已实测）
 * 生产上行路径 devlink_send_frame -> session_send -> link_send -> tcp_send
 * -> esp_tls_conn_write 全程**只发 payload**：没有 12 B 头、没有 CRC。
 * 而唯一会写 magic 的 wire_encode_header（components/wire/wire.c:91）
 * 在生产代码里**零调用**。后端每帧必经 protoframe.DecodeHeader，
 * 它强校验前两字节 magic ⇒ payload-only 一律 ErrMagic 被丢。
 *
 * ## 为什么本地全绿却漏了它（这才是根因）
 * 对锚客户端 firmware_tcp_e2e_client.c **自己** memcpy + wire_encode_header
 * ⇒ 对锚证明的是"一个会正确成帧的客户端能与后端互通"，
 * **不是**"生产固件能与后端互通"——两者是**两个不同的程序**。
 * ⇒ 修法必须同型：让对锚**调用本函数**，而不是自己成帧。
 *
 * ## 判据来源（读 RX 侧 + 后端定的，不是猜的）
 *   - ver = WIRE_VER (0x30)：wire_decode_header 强校验；后端 protoframe.Version
 *   - type = payload[0]：后端 internal/nodemgr/manager.go:418 强校验
 *     "header type == payload[0]"，不一致即丢弃并计数；后端自己的
 *     downlink.wrapFrame 也是这么取的
 *   - flags = 0（**不置 CRC32C 位**）：
 *     · 后端 CRC **校验是条件式的**（transport/server.go:483 的 if h.HasCRC()），
 *       不置位即不校验 ⇒ CRC 非必需；
 *     · 后端**自己下行也不置位**（downlink.go 的 wrapFrame 中 Header 字面量
 *       只填 Ver/Type/Seq/PayloadLen ⇒ Flags 为零值）⇒ 上行不置位才是**对称**的。
 *     · 要改这个决定：必须**先同时改后端**，否则就是在固件侧单方面发明开关。
 *   - seq：后端**不校验**（下行源码注释直言 per-node sequencing is not
 *     implemented yet）；固件 RX 侧也只是把它透传出去（rx_pump.c:103）。
 *     ⇒ 本函数取**调用方传入的** seq（纯函数，不与真机用法耦合）。
 *     真机的 seq 由调用方固定为 0，与后端下行保持一致；**未实现递增/回绕**，
 *     理由见 .c 里的说明。
 *
 * @param out        目的缓冲；须 >= 12 + payload_len
 * @param cap        out 容量
 * @param payload    2.x 载荷（首字节 = 消息类型）
 * @param payload_len 载荷长度；**0 会被拒绝**（没有首字节就没有 type）
 * @param seq        序号（写进头；后端当前不校验）
 * @param out_len    成功时写"线上总字节数"（12 + payload_len）
 * @return 0 成功；负值失败。**失败时绝不产出半条帧**（宁可不发也不发畸形帧）。
 */
int devlink_encode_frame(uint8_t *out, size_t cap,
                         const uint8_t *payload, size_t payload_len,
                         uint32_t seq, size_t *out_len);

/** 上行成帧失败的原因名（诊断用；与 devlink_encode_frame 的负返回值对应）。 */
const char *devlink_frame_err_name(int rc);

/** devlink_encode_frame 的错误码（与 wire_result_t 分开：这是"上行成帧"的判定）。 */
enum {
    DEVLINK_FRAME_OK            = 0,
    DEVLINK_FRAME_ERR_BAD_ARG   = -1,  /* 空指针 / 空载荷（无 type 字节） */
    DEVLINK_FRAME_ERR_TOO_BIG   = -2,  /* 载荷或总帧长超上限 */
    DEVLINK_FRAME_ERR_CAP       = -3,  /* out 缓冲放不下整条帧 */
    DEVLINK_FRAME_ERR_HEADER    = -4   /* wire_encode_header 拒绝（不应发生） */
};

#ifdef __cplusplus
}
#endif
#endif /* EHOME_DEVICE_LINK_WIRING_H */
