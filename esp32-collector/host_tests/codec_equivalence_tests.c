/* codec_equivalence_tests.c —— 两套独立编解码实现的**逐字节对拍**
 *
 * ## 为什么要有这个文件
 * 本仓有**两份**实现同一线路格式的编解码器：
 *   - `components/frame/frame_codec.c` —— **生产**用（8+ 个 main/components 文件在用）；
 *   - `components/msgcodec/msgcodec.c` —— S0 共享向量的 **C 侧独立实现**
 *     （`golden_vectors_tests.c` 用它校验 `protocol/vectors/wire_primitives.txt`）。
 *
 * 两份实现在设计上是**有意的**（S0 要求"三份独立实现互相校验"：
 * Go 用 `backend/pkg/frame`、C 用 msgcodec、Python 用 `check_golden_vectors.py`）。
 * **但**：共享向量只覆盖 **20 条固定用例**。超出那 20 条之后，
 * "两份实现一致"只是**假定**，没有任何检查 —— 而它们各自服务不同的调用方，
 * 一旦漂移，症状是"一端写得出、另一端读不懂"的**静默**不通。
 *
 * ⇒ 本文件把"假定一致"变成"在**大范围输入空间**上实测一致"：
 *    同一个字段序列，两条编码路径必须产出**逐字节相同**的帧；
 *    且两条解码路径必须解出**相同的值**。
 *
 * ## 覆盖的输入空间（刻意取边界，而不是随机）
 * - varint 的**长度跃变点**：0 / 1 / 127 / 128 / 16383 / 16384 / 2^32-1 / 2^64-1
 *   （1B、2B、3B、5B、10B 编码）；
 * - **tag 的长度跃变点**：field 1（tag 1 字节）与 field 16 / field 31（tag 2 字节）
 *   —— 这正是 §5 里"tag 是 (field<<3)|wire"最容易写错的地方；
 * - length-delimited 的**长度跃变点**：0 / 1 / 127 / 128 / 300 字节。
 */
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "frame_codec.h"
#include "msgcodec.h"

static int s_failures = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  ", __FILE__, __LINE__);           \
                       printf(__VA_ARGS__); printf("\n"); s_failures++; }   \
    } while (0)

#define MY_TYPE 0x22u

/* ── 用 msgcodec 编一帧（类型字节 + 字段），返回长度 ── */
static size_t encode_via_msgcodec(uint8_t *buf, size_t cap,
                                  const uint8_t *fids, const uint8_t *kinds,
                                  const uint64_t *u64v,
                                  const uint8_t *const *bv, const size_t *blen,
                                  int n)
{
    if (cap < 1) return 0;
    buf[0] = MY_TYPE;
    size_t off = 1;
    for (int i = 0; i < n; i++) {
        size_t wrote = 0;
        enc_result_t r;
        if (kinds[i] == 0) {
            r = enc_field_u64(buf + off, cap - off, &wrote, fids[i], u64v[i]);
        } else {
            r = enc_field_bytes(buf + off, cap - off, &wrote, fids[i], bv[i], blen[i]);
        }
        CHECK(r == ENC_OK, "msgcodec 编码字段 %d 失败: %s", i, enc_result_name(r));
        if (r != ENC_OK) return 0;
        off += wrote;
    }
    return off;
}

/* ── 用生产 frame_codec 编同一帧 ── */
static size_t encode_via_frame_codec(uint8_t *buf, size_t cap,
                                     const uint8_t *fids, const uint8_t *kinds,
                                     const uint64_t *u64v,
                                     const uint8_t *const *bv, const size_t *blen,
                                     int n)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, cap, MY_TYPE);
    for (int i = 0; i < n; i++) {
        frame_err_t r;
        if (kinds[i] == 0) {
            r = frame_encode_varint(&enc, fids[i], u64v[i]);
        } else {
            r = frame_encode_bytes(&enc, fids[i], bv[i], blen[i]);
        }
        CHECK(r == FRAME_OK, "frame_codec 编码字段 %d 失败: %d", i, (int)r);
        if (r != FRAME_OK) return 0;
    }
    return frame_encoder_size(&enc);
}

/* ── 用 msgcodec 解出字段序列，写回 out_* ── */
static int decode_via_msgcodec(const uint8_t *buf, size_t len,
                               uint8_t *o_fid, uint8_t *o_wt,
                               uint64_t *o_v, size_t *o_vlen, int max)
{
    size_t cursor = 1;   /* 跳过类型字节 */
    int n = 0;
    while (n < max) {
        field_view_t fv;
        dec_result_t r = dec_next_field(buf, len, &cursor, &fv);
        if (r == DEC_TRUNCATED) break;          /* 读完 */
        CHECK(r == DEC_OK, "msgcodec 解码第 %d 个字段失败: %s", n, dec_result_name(r));
        if (r != DEC_OK) return n;
        o_fid[n] = fv.field_id;
        o_wt[n]  = fv.wire_type;
        o_vlen[n] = fv.value_len;
        if (fv.wire_type == MSGCODEC_WIRE_VARINT) {
            uint64_t v = 0;
            dec_result_t r2 = dec_field_u64(&fv, &v);
            CHECK(r2 == DEC_OK, "msgcodec dec_field_u64 失败");
            o_v[n] = v;
        } else {
            o_v[n] = 0;
        }
        n++;
    }
    return n;
}

/* ── 用生产 frame_codec 解出字段序列 ── */
static int decode_via_frame_codec(const uint8_t *buf, size_t len,
                                  uint8_t *o_fid, uint8_t *o_wt,
                                  uint64_t *o_v, size_t *o_vlen, int max)
{
    frame_decoder_t dec;
    CHECK(frame_decoder_init(&dec, buf, len) == FRAME_OK, "frame_decoder_init 失败");
    int n = 0;
    while (n < max) {
        frame_field_t f;
        frame_err_t r = frame_decoder_next(&dec, &f);
        if (r != FRAME_OK) break;
        o_fid[n]  = f.field_num;
        o_wt[n]   = f.wire_type;
        o_vlen[n] = (f.wire_type == WIRE_LENGTH_DELIMITED) ? f.value.bytes.len : 0;
        o_v[n]    = (f.wire_type == WIRE_VARINT) ? f.value.varint : 0;
        n++;
    }
    return n;
}

/* ════════ 核心：同一字段序列，两条路径必须逐字节一致 ════════ */
static void check_case(const char *name,
                       const uint8_t *fids, const uint8_t *kinds,
                       const uint64_t *u64v,
                       const uint8_t *const *bv, const size_t *blen, int n)
{
    uint8_t b1[1024], b2[1024];
    size_t l1 = encode_via_frame_codec(b1, sizeof(b1), fids, kinds, u64v, bv, blen, n);
    size_t l2 = encode_via_msgcodec(b2, sizeof(b2), fids, kinds, u64v, bv, blen, n);

    CHECK(l1 == l2, "%s: 长度不一致 frame=%zu msgcodec=%zu", name, l1, l2);
    if (l1 != l2) return;
    CHECK(memcmp(b1, b2, l1) == 0, "%s: **字节不一致**（两条编码路径漂移）", name);
    if (memcmp(b1, b2, l1) != 0) {
        printf("      frame   : ");
        for (size_t i = 0; i < l1; i++) printf("%02x", b1[i]);
        printf("\n      msgcodec: ");
        for (size_t i = 0; i < l2; i++) printf("%02x", b2[i]);
        printf("\n");
        return;
    }

    /* 解码也要一致（两套解码器解出同样的字段号/wire type/值/长度） */
    uint8_t f1[32], w1[32], f2[32], w2[32];
    uint64_t v1[32], v2[32];
    size_t n1len[32], n2len[32];
    int c1 = decode_via_frame_codec(b1, l1, f1, w1, v1, n1len, 32);
    int c2 = decode_via_msgcodec(b2, l2, f2, w2, v2, n2len, 32);
    CHECK(c1 == c2, "%s: 解出字段数不一致 frame=%d msgcodec=%d", name, c1, c2);
    CHECK(c1 == n, "%s: 解出字段数 %d != 写入 %d", name, c1, n);
    int m = (c1 < c2) ? c1 : c2;
    for (int i = 0; i < m; i++) {
        CHECK(f1[i] == f2[i], "%s: 字段 %d 号不一致 %u vs %u", name, i, f1[i], f2[i]);
        CHECK(w1[i] == w2[i], "%s: 字段 %d wire type 不一致 %u vs %u", name, i, w1[i], w2[i]);
        CHECK(v1[i] == v2[i], "%s: 字段 %d 值不一致 %llu vs %llu", name, i,
              (unsigned long long)v1[i], (unsigned long long)v2[i]);
        /* 负载长度要**同口径**比：
         *   - length-delimited：两边都直接给字节数；
         *   - varint：frame_codec 只给"值"，msgcodec 给"varint 字节数"
         *     ⇒ 用 frame_varint_size(值) 复原 frame 侧的字节数再比（更强：
         *       它同时验了"值"与"编码长度"两条）。 */
        if (w1[i] == WIRE_VARINT) {
            CHECK(frame_varint_size(v1[i]) == n2len[i],
                  "%s: 字段 %d varint 编码长度不一致 frame=%zu(msgcodec=%zu)",
                  name, i, frame_varint_size(v1[i]), n2len[i]);
        } else {
            CHECK(n1len[i] == n2len[i], "%s: 字段 %d 负载长度不一致 %zu vs %zu",
                  name, i, n1len[i], n2len[i]);
        }
    }
}

int main(void)
{
    /* ---- 1. varint 的长度跃变点（1/2/3/5/10 字节编码）---- */
    static const uint64_t vs[] = { 0u, 1u, 127u, 128u, 16383u, 16384u,
                                   0xFFFFFFFFull, 0xFFFFFFFFFFFFFFFFull };
    static const char *vnames[] = { "varint_0", "varint_1", "varint_127", "varint_128",
                                    "varint_16383", "varint_16384", "varint_u32max",
                                    "varint_u64max" };
    for (unsigned i = 0; i < sizeof(vs) / sizeof(vs[0]); i++) {
        uint8_t fid[1] = { 1 }, kind[1] = { 0 };
        uint64_t uv[1] = { vs[i] };
        const uint8_t *bv[1] = { NULL }; size_t bl[1] = { 0 };
        check_case(vnames[i], fid, kind, uv, bv, bl, 1);
    }

    /* ---- 2. tag 的长度跃变点（field 1 = 1 字节 tag；16/31 = 2 字节 tag）---- */
    {
        uint8_t fid[3] = { 1, 16, 31 }, kind[3] = { 0, 0, 0 };
        uint64_t uv[3] = { 7, 8, 9 };
        const uint8_t *bv[3] = { NULL, NULL, NULL }; size_t bl[3] = { 0, 0, 0 };
        check_case("tag_1byte_then_2byte", fid, kind, uv, bv, bl, 3);
    }

    /* ---- 3. length-delimited 的长度跃变点（含空与跨 varint 边界）---- */
    static uint8_t big[300];
    for (size_t i = 0; i < sizeof(big); i++) big[i] = (uint8_t)(i & 0xFF);
    {
        uint8_t fid[5] = { 1, 2, 3, 4, 5 }, kind[5] = { 1, 1, 1, 1, 1 };
        uint64_t uv[5] = { 0, 0, 0, 0, 0 };
        const uint8_t *bv[5] = { (const uint8_t *)"", big, big, big, big };
        size_t bl[5] = { 0, 1, 127, 128, 300 };
        check_case("bytes_len_jumps", fid, kind, uv, bv, bl, 5);
    }

    /* ---- 4. 混合：varint 与 bytes 交错（真实消息形状）---- */
    {
        uint8_t fid[4] = { 1, 2, 3, 9 }, kind[4] = { 0, 1, 0, 0 };
        uint64_t uv[4] = { 4, 0, 7, 2712847316ull };
        const uint8_t *bv[4] = { NULL, (const uint8_t *)"v3-link-node", NULL, NULL };
        size_t bl[4] = { 0, 12, 0, 0 };
        check_case("mixed_fields", fid, kind, uv, bv, bl, 4);
    }

    /* ---- 5. 反向对照：故意造一个不一致，确认本用例**真的会比较字节** ----
     * 不跑真实不一致（那要改生产代码）；这里只验证"比较逻辑本身有效"：
     * 同一帧编两次必须相同（若比较逻辑写反，这条也过不了）。 */
    {
        uint8_t fid[1] = { 3 }, kind[1] = { 0 };
        uint64_t uv[1] = { 2712847316ull };
        const uint8_t *bv[1] = { NULL }; size_t bl[1] = { 0 };
        uint8_t x[64], y[64];
        size_t lx = encode_via_frame_codec(x, sizeof(x), fid, kind, uv, bv, bl, 1);
        size_t ly = encode_via_frame_codec(y, sizeof(y), fid, kind, uv, bv, bl, 1);
        CHECK(lx == ly && memcmp(x, y, lx) == 0, "同一编码器两次结果应相同（自检）");
        /* ⚠ 7 = 1 类型字节 + 1 tag + 5 varint。
         * 我第一版写成 6（漏了类型字节）—— 与 msgcodec.h 头部记录的那次错误**同型**
         * （"12 条里错 11 条，全部差'少一个类型字节'"）。这类错在本仓已出现两次
         * ⇒ 凡算"帧长度"的地方，先问**含不含类型字节**。 */
        CHECK(lx == 7, "field 3 的 u64 2712847316 应编成 7 字节"
                       "（1 类型 + 1 tag + 5 varint），实际 %zu", lx);
    }

    if (s_failures) { printf("codec_equivalence_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("codec_equivalence_tests: 两套编解码实现在全部边界输入上逐字节一致（all checks passed）\n");
    return 0;
}
