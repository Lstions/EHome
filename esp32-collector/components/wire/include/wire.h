/**
 * @file wire.h
 * @brief 帧定界（delimiting）—— 3.0 帧格式（原则 P2/P3）
 *
 * 为什么需要它：TCP 是字节流。旧代码把一次 recv() 的内容直接当【一条完整消息】
 * 回调（ehome_tcp.c:447），而 transport.h 从未声明这个前提 —— MQTT 恰好满足、
 * TCP 流不满足（D-09）。本模块把"定界"变成【显式契约】。
 *
 * ## 帧格式（12 B 定长头）
 *
 *     偏移  字段          宽度
 *     0     magic         2     = 0x4548 ("EH")
 *     2     ver           1     = 0x30 (3.0)
 *     3     type          1     消息类型
 *     4     flags         2     见下方 flags 位
 *     6     seq           4     每方向单调递增（重放检测 + ACK 关联）
 *     10    payload_len   2     本条消息的载荷长度（**这就是分界**）
 *     ---- 共 12 B ---- 之后是 payload_len 字节载荷；flags.CRC32C 置位时再跟 4 B
 *
 * ## ⚠ 与前一版（16 B 头 + 分片）的差异 —— 这是**有意删除**
 * 前一版把头做成 16 B：magic ver type flags seq **frag_off frag_len total_len**，
 * 并实现了一套"一条消息拆成多片、收方按 frag_off 拼回"的分片/重组机制。
 *
 * **经用户决策（2026-10-06）：不需要分片与重组，需要的是分界。**
 * 删除的依据（见 docs/设计/决策-帧格式是否需要分片-2026-10-06.md）：
 *   1. TCP 是可靠有序流 ⇒ 分片要防的"乱序/丢片/重复"**在 TCP 上不会发生**；
 *      原判定表 8 条里有 4 条在防 TCP 已排除的状态（文档自陈"TCP 保证有序/不丢"）；
 *   2. 分片唯一多出的能力（交错 / 增量消费 / 跨传输复用）本设计**都不做**：
 *      in-flight=1 明令禁止交错；T1 明确"不依赖 BLE"；收到即整流交付；
 *   3. **实测**：TCP 不保留应用层 write 边界（3 x 1024 B 到达为 2 次 recv，
 *      且分界位置不可预测）⇒ "一次 recv 拿到一片"这个前提本就不成立，
 *      分片换不来任何定界能力（tools/verify_tcp_is_stream.py 可复跑）；
 *   4. ⇒ 分片的乱序/丢片/重复分支**在 TCP 上永不触发**，
 *      属本项目反复出事的"不可达代码"形态（对照 D-03/D-09/D-10）。
 *
 * **保留的**：定界本身（长度前缀）+ magic/ver 校验 + seq + flags + 可选 CRC。
 * **删除的**：frag_off、frag_len、MORE 位、多片状态机、丢片超时、乱序/重复判定。
 *
 * ## ⚠ 端序：设计未规定，本实现定为【大端】（网络字节序）
 * 设计 §5.1 全篇没有规定字节序。多字节字段没有字节序 ⇒ 两端各自"按本机来"
 * 也能各自自测通过，却会**互相读错**。定为大端：线路协议通行约定；
 * 且与两端主机字节序无关（ESP32/C6 与常见服务器都是小端，定小端则
 * "能跑"只因双方恰好同序，换一端就静默错）。唯一向量来源见
 * protocol/vectors/frame_header.txt。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_WIRE_H
#define EHOME_WIRE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 帧头长度（定长）。 */
#define WIRE_HEADER_BYTES 12u

/** magic/ver。 */
#define WIRE_MAGIC 0x4548u
#define WIRE_VER   0x30u

/** 载荷硬上界。
 *
 * 取 16368 = MBEDTLS_SSL_IN_CONTENT_LEN(16384) - 头(12) - CRC(4)。
 * 这样"**一条完整消息恰好放进一个 TLS 记录**"这个性质**是真的**。
 *
 * 为什么强调：上一版把上界写成 16384（=14 + 16384 + 4 > 16384），
 * 于是它用来论证"整条进一个记录"的那句话**差了 20 B 而不成立**
 * （见决策文档 §11.2）。现在把上界反过来从 TLS 记录倒推，性质才成立。
 */
#define WIRE_PAYLOAD_MAX 16368u

/** flags 位。
 * 注：上一版的 bit0 是 MORE（还有后续片）；分片删除后该位消失，
 * 其余位**重新编号**以避免留下"这是什么？"的空位。 */
#define WIRE_FLAG_ACK_REQ 0x0001u  /* 请求对端回 ACK */
#define WIRE_FLAG_IS_ACK  0x0002u  /* 本条是 ACK */
#define WIRE_FLAG_IS_NAK  0x0004u  /* 本条是 NAK */
#define WIRE_FLAG_CRC32C  0x0008u  /* 载荷之后跟 4 B CRC32C */

/** 可选 CRC32C 的长度（整条消息）。 */
#define WIRE_CRC_BYTES 4u

/** 已解码的帧头（字段已转主机序）。 */
typedef struct {
    uint8_t  ver;
    uint8_t  type;
    uint16_t flags;
    uint32_t seq;
    uint16_t payload_len;
} wire_header_t;

/** 帧头编解码结果。 */
typedef enum {
    WIRE_OK = 0,
    WIRE_ERR_SHORT,      /* 字节不足 WIRE_HEADER_BYTES（需要更多数据；不是错误） */
    WIRE_ERR_MAGIC,      /* magic 不符 */
    WIRE_ERR_VERSION,    /* ver 不符 */
    WIRE_ERR_RANGE,      /* payload_len > WIRE_PAYLOAD_MAX */
    WIRE_ERR_STRUCTURE,  /* 保留：当前无结构性错误可判（分片删除后） */
    WIRE_ERR_BAD_ARG
} wire_result_t;

const char *wire_result_name(wire_result_t r);

/**
 * 编码帧头（大端）。out 必须至少 WIRE_HEADER_BYTES。
 */
wire_result_t wire_encode_header(uint8_t *out, size_t cap, const wire_header_t *h);

/**
 * 解码帧头（大端）。只读 in[0, WIRE_HEADER_BYTES)。
 * 校验 magic / ver / payload_len 上界。
 */
wire_result_t wire_decode_header(const uint8_t *in, size_t n, wire_header_t *out);

/** CRC32C（Castagnoli，反射多项式 0x82F63B78）。
 * 标准校验值：wire_crc32c("123456789", 9) == 0xE3069283（由 host 测试断言）。 */
uint32_t wire_crc32c(const uint8_t *data, size_t n);

/** 该头是否带 CRC32C（按 flags 判断）。 */
bool wire_header_has_crc(const wire_header_t *h);

/** 本条消息在线上占用的总字节数（头 + 载荷 + 可选 CRC）。 */
uint32_t wire_frame_wire_bytes(const wire_header_t *h);

/* === 流定界器（delimiter）===
 *
 * 语义：把无边界字节流切成语义完整的消息。**不做重组** —— 因为不存在分片。
 */
typedef enum {
    WIRE_DELIM_NEED_MORE = 0,  /* 数据不足，等更多字节 */
    WIRE_DELIM_MSG_READY,      /* 产出一条完整消息（payload_out/len_out 有效） */
    WIRE_DELIM_ERR_TOO_LARGE,  /* 声明的长度超上界：**立即拒绝，不缓冲** */
    WIRE_DELIM_ERR_MALFORMED,  /* 头非法（magic/ver/结构） */
    WIRE_DELIM_ERR_CRC,        /* CRC32C 校验失败 */
    WIRE_DELIM_ERR_INTERNAL
} wire_delim_result_t;

const char *wire_delim_result_name(wire_delim_result_t r);

typedef struct wire_delim wire_delim_t;

/**
 * 创建定界器。max_payload 是构造参数（P5：上界来自单一来源，不散落常量）。
 * 内部缓冲 = max_payload + 头 + CRC，一次分配；不存在按对端声明扩容。
 */
wire_delim_t *wire_delim_create(uint32_t max_payload);

/**
 * task-34：用**调用方提供**的缓冲创建定界器（缓冲不归本模块所有，销毁时不释放）。
 *
 * 用途：在 PSRAM 型号上把这块**纯数据**缓冲放到外部 RAM，把内部连续块还给
 * 内存门禁（placement 属 P8 允许的差异，不改任何可观测行为）。
 * 本文件保持 IDF 无关，所以由调用方决定池。
 *
 * @param buf 调用方拥有；cap 必须 >= max_payload + 头 + CRC，否则返回 NULL。
 */
wire_delim_t *wire_delim_create_with_buf(uint32_t max_payload, uint8_t *buf, size_t cap);

void         wire_delim_destroy(wire_delim_t *d);

/**
 * 投入 n 字节，最多产出一条完整消息。
 *
 * 确定性行为（由 host_tests/wire_tests.c 逐条锁定）：
 *   - 数据不足          -> NEED_MORE（不产出、不丢字节，继续累积）；
 *   - 头非法            -> ERR_MALFORMED，**丢弃已累积数据**（重新同步）；
 *   - payload_len 超上界 -> ERR_TOO_LARGE，**不缓冲**（禁止"按对端声明分配"）；
 *   - 收齐              -> MSG_READY，*payload_out 指向**内部**缓冲（零拷贝），
 *                          在下一次 feed 之前有效；
 *   - 出错后下一次 feed 先复位再处理（错误不粘住）。
 *
 * 注意：本函数**不是**可重入的重组器 —— 它只保证"一次 feed 最多出一条"。
 */
wire_delim_result_t wire_delim_feed(wire_delim_t *d, const uint8_t *in, size_t n,
                                    const uint8_t **payload_out, size_t *len_out);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_WIRE_H */
