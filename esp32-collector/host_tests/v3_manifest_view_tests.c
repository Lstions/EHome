/**
 * v3_manifest_view_tests.c — PROTOTYPE tests (WS-F phase 2, host only)
 *
 * Proves the design claim: a ConfigManifest can be retained as raw bytes + a
 * fixed offset index and served through accessors, without a config_manifest_t
 * (5,172 B on the 32-bit device ABI). Also measures the index footprint and
 * the retained-bytes ratio so the design document's numbers are reproducible.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "frame_codec.h"
#include "v3_manifest_view.h"
#include "config_mgr.h"

static int tests_run, tests_passed;
#define CHECK(cond, msg) do { \
    tests_run++; \
    if (cond) tests_passed++; else printf("FAIL [%d]: %s\n", tests_run, msg); \
} while (0)
static int tests_passed;

/* --- minimal encoder (test-local; mirrors frame_codec semantics) --- */
static size_t pv(uint8_t *b, uint64_t v)
{
    size_t n = 0;
    while (v > 0x7F) { b[n++] = (uint8_t)((v & 0x7F) | 0x80); v >>= 7; }
    b[n++] = (uint8_t)v;
    return n;
}
static size_t pb(uint8_t *b, uint8_t f, const uint8_t *d, size_t n)
{
    size_t o = pv(b, ((uint64_t)f << 3) | 2);
    o += pv(b + o, n);
    memcpy(b + o, d, n);
    return o + n;
}
static size_t pvi(uint8_t *b, uint8_t f, uint64_t v)
{
    size_t o = pv(b, ((uint64_t)f << 3) | 0);
    return o + pv(b + o, v);
}

/* Build a manifest frame matching the backend encoder's layout. */
static size_t build_manifest_ex(uint8_t *buf, size_t cap, int nch, int ntmpl, int nested, int ntids)
{
    uint8_t body[8192];
    size_t bl = 0;
    const char *mid = "mfst-v3-proto-0001";
    const char *sid = "sync-v3-proto-0001";
    bl += pb(body + bl, 1, (const uint8_t *)mid, strlen(mid));
    bl += pb(body + bl, 8, (const uint8_t *)sid, strlen(sid));

    for (int i = 0; i < ntmpl; i++) {
        uint8_t t[128]; size_t tl = 0;
        tl += pvi(t + tl, 1, (uint64_t)(i + 1));
        uint8_t wd[8] = {0xAA};
        tl += pb(t + tl, 2, wd, sizeof wd);
        tl += pvi(t + tl, 3, 256);
        bl += pb(body + bl, 3, t, tl);
    }
    for (int i = 0; i < nch; i++) {
        uint8_t c[1024]; size_t cl = 0;
        uint8_t bus[9] = {1, 2, 0, 0, 0, 0, 7, 8, 9};
        cl += pvi(c + cl, 1, (uint64_t)(i + 1));
        cl += pvi(c + cl, 5, 1);
        cl += pvi(c + cl, 6, 3);
        cl += pb(c + cl, 7, bus, sizeof bus);
        cl += pvi(c + cl, 8, 1);
        for (int k = 0; k < ntids; k++) cl += pvi(c + cl, 3, (uint64_t)(k + 1)); /* template_ids */
        for (int e = 0; e < nested; e++) {
            uint8_t g[256]; size_t gl = 0;
            gl += pvi(g + gl, 1, (uint64_t)(100 + e));
            gl += pvi(g + gl, 2, (uint64_t)(e + 1));
            for (int k = 0; k < 3; k++) {
                uint8_t cmd[32]; size_t ml = 0;
                ml += pvi(cmd + ml, 1, 7);
                ml += pvi(cmd + ml, 2, 100);
                ml += pvi(cmd + ml, 3, 1);
                gl += pb(g + gl, 3, cmd, ml);
            }
            cl += pb(c + cl, 9, g, gl);
        }
        bl += pb(body + bl, 4, c, cl);
    }
    if (bl + 2 > cap) return 0;
    buf[0] = MSG_CONFIG_MFST;
    memcpy(buf + 1, body, bl);
    return bl + 1;
}

static size_t build_manifest(uint8_t *buf, size_t cap, int nch, int ntmpl, int nested)
{
    return build_manifest_ex(buf, cap, nch, ntmpl, nested, 1);
}

static void test_index_and_accessors(void)
{
    static uint8_t frame[8192];
    size_t len = build_manifest(frame, sizeof frame, 5, 16, 5);
    CHECK(len > 0 && len <= 2048, "typical manifest fits in the 2 KiB downlink budget");

    printf("MEASURE typical frame bytes = %zu\n", len);
    v3mv_t v;
    CHECK(v3mv_init(&v, frame, len) == V3MV_OK, "init parses the frame");
    CHECK(v.channel_count == 5, "5 channels indexed");
    CHECK(v.template_count == 16, "16 templates indexed");
    char idbuf[64];
    CHECK(v3mv_manifest_id(&v, idbuf, sizeof idbuf) == V3MV_OK &&
          strcmp(idbuf, "mfst-v3-proto-0001") == 0, "manifest_id bounded copy");
    CHECK(v3mv_sync_id(&v, idbuf, sizeof idbuf) == V3MV_OK &&
          strcmp(idbuf, "sync-v3-proto-0001") == 0, "sync_id bounded copy");
    CHECK(v3mv_manifest_id(&v, idbuf, 4) == V3MV_ERR_TOO_MANY,
          "short output buffer rejected, not clipped");

    v3mv_channel_t ch;
    CHECK(v3mv_get_channel(&v, 0, &ch) == V3MV_OK, "channel 0 parses");
    CHECK(ch.id == 1 && ch.bus_type == 3 && ch.enabled, "channel 0 scalars");
    CHECK(ch.bus_config.len == 9, "channel 0 bus_config span");
    CHECK(ch.template_count == 1 && ch.edge_count == 5, "channel 0 repeated spans");

    v3mv_edge_t e;
    CHECK(v3mv_get_edge(&v, &ch, 4, &e) == V3MV_OK, "last edge parses");
    CHECK(e.edge_device_id == 104 && e.command_count == 3, "edge scalars + command count");

    v3mv_command_t cmd;
    CHECK(v3mv_get_command(&v, &ch, 4, 2, &cmd) == V3MV_OK, "last command parses");
    CHECK(cmd.template_id == 7 && cmd.interval_ms == 100 && cmd.enabled, "command scalars");

    v3mv_span_t wd; uint32_t rl = 0;
    CHECK(v3mv_find_template(&v, 16, &wd, &rl) == V3MV_OK, "template lookup by id");
    CHECK(wd.len == 8 && rl == 256, "template write_data/read_length");
    CHECK(v3mv_find_template(&v, 99, &wd, &rl) == V3MV_ERR_REQUIRED, "missing template rejected");
}

static void test_fail_closed(void)
{
    static uint8_t frame[8192];
    size_t len = build_manifest(frame, sizeof frame, 2, 2, 0);
    v3mv_t v;
    CHECK(v3mv_init(&v, frame, len) == V3MV_OK, "small manifest parses");
    CHECK(v3mv_init(&v, frame, 3) == V3MV_ERR_TRUNCATED,
          "frame shorter than its declared lengths is TRUNCATED, not WIRE");
    frame[0] = MSG_DATA_RPT;
    CHECK(v3mv_init(&v, frame, len) == V3MV_ERR_TYPE, "wrong message type rejected");
    frame[0] = MSG_CONFIG_MFST;

    /* every repeated field bound must fail closed, not clip */
    static uint8_t big[16384];
    size_t n = build_manifest(big, sizeof big, V3MV_MAX_CHANNELS + 1, 2, 0);
    CHECK(n > 0 && v3mv_init(&v, big, n) == V3MV_ERR_TOO_MANY, "over-limit channels rejected");
    n = build_manifest(big, sizeof big, 2, V3MV_MAX_TEMPLATES + 1, 0);
    CHECK(n > 0 && v3mv_init(&v, big, n) == V3MV_ERR_TOO_MANY, "over-limit templates rejected");
    n = build_manifest(big, sizeof big, 1, 1, V3MV_MAX_EDGES + 1);
    v3mv_channel_t ch;
    CHECK(n > 0 && v3mv_init(&v, big, n) == V3MV_OK &&
          v3mv_get_channel(&v, 0, &ch) == V3MV_ERR_TOO_MANY, "over-limit edges rejected");
    n = build_manifest_ex(big, sizeof big, 1, 1, 0, V3MV_MAX_TEMPLATE_IDS + 1);
    CHECK(n > 0 && v3mv_init(&v, big, n) == V3MV_OK &&
          v3mv_get_channel(&v, 0, &ch) == V3MV_ERR_TOO_MANY,
          "over-limit template_ids rejected before writing the slot");

    /* A nested length-delimited field that declares more bytes than the frame
     * actually contains must fail closed at accessor time, never be recorded
     * as a truncated/garbage span. Hand-craft a minimal manifest: manifest_id,
     * then one channel whose bus_config (field 7) declares 0x8000 bytes. */
    {
        static uint8_t tiny[96];
        size_t o = 0;
        tiny[o++] = MSG_CONFIG_MFST;
        o += pb(tiny + o, 1, (const uint8_t *)"m1", 2);   /* manifest_id */
        uint8_t chbuf[32]; size_t cl = 0;
        cl += pvi(chbuf + cl, 1, 1);              /* id */
        cl += pvi(chbuf + cl, 6, 1);              /* bus_type */
        cl += pv(chbuf + cl, (7u << 3) | 2);      /* field 7, LEN */
        cl += pv(chbuf + cl, 0x8000u);            /* declared 32768 B, absent */
        o += pb(tiny + o, 4, chbuf, cl);          /* channel (field 4) */
        CHECK(v3mv_init(&v, tiny, o) == V3MV_OK, "truncated bus_config frame indexes");
        CHECK(v3mv_get_channel(&v, 0, &ch) == V3MV_ERR_TRUNCATED,
              "declared length beyond the buffer fails closed at accessor");
    }

    /* A legitimate field longer than the 16-bit span slot must be refused,
     * never silently truncated. 65,536 B is wire-legal but far past anything
     * this design admits, so the guard is defense-in-depth: it must bite while
     * indexing, before any accessor runs. */
    {
        const size_t big_len = 65536;
        uint8_t *huge = (uint8_t *)malloc(big_len + 4096);
        uint8_t *chbuf = (uint8_t *)malloc(big_len + 64);
        CHECK(huge != NULL && chbuf != NULL, "alloc oversized-field probe");
        if (huge && chbuf) {
            size_t o = 0;
            huge[o++] = MSG_CONFIG_MFST;
            o += pb(huge + o, 1, (const uint8_t *)"m1", 2);
            size_t cl = 0;
            cl += pvi(chbuf + cl, 1, 1);
            cl += pvi(chbuf + cl, 6, 1);
            cl += pv(chbuf + cl, (7u << 3) | 2);
            cl += pv(chbuf + cl, big_len);
            memset(chbuf + cl, 0, big_len);
            cl += big_len;
            o += pb(huge + o, 4, chbuf, cl);
            CHECK(v3mv_init(&v, huge, o) == V3MV_ERR_TOO_MANY,
                  "repeated field >UINT16_MAX refused while indexing");
        }
        free(chbuf);
        free(huge);
    }
}

static void test_footprint(void)
{
    /* This translation unit is compiled WITHOUT a target macro, so
     * MAX_CHANNELS falls back to 8 -- the generic upper bound, not the
     * S3/C6 device value. Device figures (S3=5 / C6=4) are in the design doc. */
    printf("v3mv_t index bytes (generic 8ch) = %zu\n", v3mv_index_bytes());
    printf("config_manifest_t (this build)   = %zu\n", sizeof(config_manifest_t));
    printf("HEAD baseline config_manifest_t  = 5172 (32-bit ABI, MAX_CHANNELS=8)\n");
    printf("2x(config_manifest_t) staging    = %zu (this build)\n",
           (size_t)(2 * sizeof(config_manifest_t)));
}

int main(void)
{
    test_index_and_accessors();
    test_fail_closed();
    test_footprint();
    printf("\nv3_manifest_view_tests: %d/%d passed\n", tests_passed, tests_run);
    return tests_passed == tests_run ? 0 : 1;
}
