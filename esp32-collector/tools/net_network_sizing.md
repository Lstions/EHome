# WS-D（task-4）网络定容 / IPv6 / BLE 预算 / 100 Hz 吞吐验证 — 交付设计说明

日期：2026-10-05　分支：`feat/mem-psram-phase1`（worktree `mem-psram`）
改动文件：`sdkconfig.defaults`、`sdkconfig.defaults.esp32s3psram`（仅 WiFi/LWIP 段落）、
`tools/net_throughput_test.py`、`tools/net_harness_selftest.py`、本文件。

> 本文件的所有数字都有来源：IDF 6.1 源码行号、构建产物 map/ELF、或本机能跑的实测。
> 未在实机验证的部分明确标注"待实机"。

---

## 1. 吞吐模型（这些值为什么是这些值）

### 1.1 需求

| 量 | 值 | 来源 |
|---|---|---|
| 满配通道数 | 5（3 UART + SPI + I2C） | `components/hw_profile/hw_tables.c:111-118`（S3） |
| 每通道采样率 | 100 Hz（`interval_ms=10`） | 任务书；`components/scheduler/scheduler.c:782` 支持 `<100ms` 时切 1 ms tick |
| 事务率 | 5 × 100 = **500 txn/s** | 推导 |
| 每样本 DataReport 裸载荷 | ≈ **300 B** | 任务书给定；本仓 `REPORT_PAYLOAD_BLOCK_SIZE=1024` 是上限块，实际样本 ~300 B |
| 上行裸载荷 | 500 × 300 B = **150 KB/s** | 推导 |
| 设计点 | **300 KB/s（2×）** | 给重传、ACK、MQTT keepalive/控制、OTA 并发留余量 |

### 1.2 为什么链路速率不是瓶颈

802.11n 2.4 GHz 在繁忙家用 mesh 下仍有数 Mbit/s 有效吞吐（IDF Wi-Fi 性能指南；
本仓历史压测也远高于此）。因此约束是**缓冲/排队**，不是链路速率。定容的目标是：
在 300 KB/s 设计点上，每个可能的排队点仍留 4× 左右余量，而不是贴着 150 KB/s 均值砍。

### 1.3 每个 Kconfig 值

| 符号 | 原值 | 新值 | 为什么 | 对应余量 |
|---|---:|---:|---|---|
| `ESP_WIFI_STATIC_RX_BUFFER_NUM` | 10 | **6** | 硬件 DMA ring，每块 ~1.6 KB（`esp_wifi/Kconfig:32`） | 6×1.6 KB = 9.6 KB ÷ 300 KB/s ≈ **32 ms** 突发吸收；且等于 `RX_BA_WIN=6`（IDF 建议 static ≥ BA window，`esp_wifi/Kconfig:37-39`） |
| `ESP_WIFI_DYNAMIC_RX_BUFFER_NUM` | 32 | **16** | WiFi 层动态 RX，按帧长分配 | IDF 峰值公式 `p=b_rx*m_rx+b_tx*m_tx`（Wi-Fi 性能指南 "Peak Wi-Fi Dynamic Buffer"）；16 块给 ~85 ms@300 KB/s |
| `ESP_WIFI_DYNAMIC_TX_BUFFER_NUM` | 32 | **16** | s3/c6 上 TX 动态缓冲减半。**s3p 上该符号不存在**（IDF 强制 static TX，见 §3） | s3p 用 STATIC_TX=16+CACHE_TX=32 |
| `ESP_WIFI_MGMT_SBUF_NUM` | 32 | **16** | 关联/扫描/控制短缓冲，事件驱动不是 100 Hz 流 | IDF 最小 6（`esp_wifi/Kconfig:265`），16 是 2.6× |
| `LWIP_TCPIP_RECVMBOX_SIZE` | 32 | **16** | TCPIP 任务邮箱；300 KB/s 下每段一条消息，16 条 ≈ 50 ms@满速；且必须 > TCP/UDP recvmbox | 同上 |
| `LWIP_MAX_SOCKETS` | 10 | **6** | 纯 TCP 客户端：MQTT + OTA(TLS) + 瞬时 DNS，最多 3 并发 | 2× |
| `LWIP_MAX_ACTIVE_TCP` | 16 | **6** | 同上 | 2× |
| `LWIP_MAX_LISTENING_TCP` | 16 | **2** | 生产无监听端口（DEBUG_TCP 关） | 已富余 |
| `LWIP_UDP_RECVMBOX_SIZE` | 6 | **6**（任务卡写 4，**不可实现**） | 只有 DNS/DHCP 请求-响应；IDF 硬范围 6..64（`lwip/Kconfig:859-862`），写 4 被 kconfig **静默夹到 6**（已实测 reconfigure 后落 6） | 下限即最优 |
| `LWIP_TCP_SND_BUF_DEFAULT` / `LWIP_TCP_WND_DEFAULT` | 5760 / 5760 | **保持不变** | 4×MSS，是 MQTT 上行吞吐/时延敏感对；砍它=用 RAM 换 p99，违反 100 Hz 要求 | 不动 |
| `MQTT_BUFFER_SIZE` / outbox 4096 | 2048 / 4096 | **保持不变** | outbox 4096 可容 ~10–40 条未确认 QoS1（100–400 B/帧）= 20–80 ms@500 Hz，与 5760 TCP 窗口匹配 | 不动 |
| `DEBUG_TCP_ENABLED` | y | **n** | 生产关闭（Kconfig 保留可开；关时 `#ifdef` 完全移出镜像） | flash |

**关于 STATIC_RX=6 的取舍**：任务卡允许上调到 8。我维持 6，因为：
(a) 6 已给 32 ms 突发吸收，远大于一个 10 ms 采样周期的排队；(b) 100 Hz 流是"平滑"的，
突发主要来自 OTA 并发时的下行 ACK 风暴，那部分由动态 RX 16 承担；
(c) 若实机压测出现 `report_drop>0` 或 WiFi 丢包，再回调（lead 已同意此触发条件）。

### 1.4 功能开关（2026-10-05 用户决议）

新增/确认：

```
CONFIG_LWIP_DHCPS=n                         # STA only
CONFIG_ESP_WIFI_SOFTAP_SUPPORT=n            # STA only
CONFIG_LWIP_DNS_SUPPORT_MDNS_QUERIES=n      # 无 mDNS 调用（仓库 grep 无命中）
CONFIG_ESP_WIFI_ENTERPRISE_SUPPORT=n
CONFIG_ESP_WIFI_ENABLE_WPA3_OWE_STA=n
CONFIG_ESP_WIFI_ENABLE_WPA3_OWE_SOFTAP=n
CONFIG_ESP_WIFI_ENABLE_SAE_PK=n
CONFIG_ESP_WIFI_SOFTAP_SAE_SUPPORT=n
CONFIG_ESP_WIFI_ENABLE_WPA3_SAE=y           # 保留 Personal
CONFIG_ESP_WIFI_ENABLE_SAE_H2E=y
CONFIG_ESP_WIFI_WPA3_COMPATIBLE_SUPPORT=y
CONFIG_DEBUG_TCP_ENABLED=n
```

依据：
- `LWIP_DHCPS` 有 `#if ESP_DHCPS` 保护（`lwip/port/include/lwipopts.h:1716`、`esp_netif_lwip.c:865/1083/1175`），单独关安全。
- **`WIFI_AUTH_WPA2_PSK` 阈值保持不动**：`wifi_mgr.c:121` 的 threshold 语义是"接受的**最低**安全等级"，
  混合模式注释明确 "the minimum mode becomes the stronger of the two"（`esp_wifi_types_generic.h:375/377`）。
  改成 WPA2_WPA3 会把门槛抬到 WPA3，**反而拒绝纯 WPA2 AP**。PMF 站端默认 Optional
  （`docs/en/api-guides/wifi-security.rst:140`），SAE PWE 默认 BOTH（`esp_wifi_types_generic.h:549/582`），均无需改。
  WPA3 验收改为实机三类 AP：WPA3-only / WPA2-only / WPA2-WPA3 混合。

### 1.5 ⚠ SoftAP=n 的已知链接地雷（已实测，归 task-8 落地）

`components/wifi_mgr/wifi_mgr.c:110` 在无凭据时调用 `wifi_mgr_start_provisioning()`，
后者调用 `esp_netif_create_default_wifi_ap()`（`wifi_mgr.c:435`）。该 API 在
`CONFIG_ESP_WIFI_SOFTAP_SUPPORT=n` 时**不存在**（`esp_wifi/src/wifi_default.c:415-427` 整段在 `#ifdef` 内）。

实测证据：
- 默认 SSID="ABC"（非空）时，`if (def_ssid[0] != '\0')` 编译期恒真 → provisioning 分支被死代码消除 → **能链接**。
- 把 `CONFIG_COLLECTOR_WIFI_SSID=""`（真实"首启无凭据"场景）后，构建直接失败：
  ```
  ld: libwifi_mgr.a(wifi_mgr.c.obj): undefined reference to `esp_netif_create_default_wifi_ap'
  ```
即当前默认 SSID 在**掩盖**这个缺陷。task-8 必须用 `#if CONFIG_ESP_WIFI_SOFTAP_SUPPORT`
包住整条 provisioning 路径，改为"告警 + 状态位 + 等待 BLE/NVS 凭据"，并把
"空 SSID + SOFTAP=n 必须构建通过"作为验收硬条件（lead 已登记）。

---

## 2. IPv6 开关与"真实可连"

### 2.1 实现方式

`CONFIG_COLLECTOR_IPV6`（默认 y，task-1 加入 `main/Kconfig.projbuild`）通过
**Kconfig `select`** 联动，唯一事实来源在 Kconfig（task-11）：

```
select LWIP_IPV6
select LWIP_IPV6_AUTOCONFIG   # SLAAC；IDF 6.1 默认 n (lwip/Kconfig:522-525)
```

已在本机隔离工程实测：`COLLECTOR_IPV6=y → LWIP_IPV6=y + LWIP_IPV6_AUTOCONFIG=y`；
`=n → 两者都 n`。三个 profile 的最终 sdkconfig 均确认生效。
**没有**在 defaults 里重复写这两个值（避免双头维护，lead 已裁决）。

### 2.2 ⚠ 发现：仅开 IPv6 还不足以"真实可连"（归 task-8）

`components/wifi_mgr/wifi_mgr.c:544-549` 只在 **`IP_EVENT_STA_GOT_IP`（IPv4）** 时
`set_state(WIFI_MGR_CONNECTED)` 并置 `WIFI_CONNECTED_BIT`。而 IPv6 全局地址走的是
**另一个事件** `IP_EVENT_GOT_IP6`（`esp_netif/lwip/esp_netif_lwip.c:2380`，
`esp_netif_internal_nd6_cb()` 发出）。固件没有注册它。

后果：**IPv6-only 或纯 SLAAC 场景下，设备拿到全局 IPv6 也不会进入 CONNECTED，
MQTT supervisor 不启动**。这不是 Kconfig 能解决的。

运维语义（lead 已采纳）：本期"真实可连"的验收拆成
1. **双栈**：IPv4 GOT_IP 后 MQTT 上线；且 broker 用 IPv6 字面量（`mqtt://[2408:...]:1883`）
   或域名 AAAA 解析时连接成功；
2. **IPv6-only**：需要 task-8 注册 `IP_EVENT_GOT_IP6` 并作为"可用判据之一"（已登记），
   本期标注为已知缺口。

### 2.3 IPv6 验证方案（脚本已具备，实机标注"待实机"）

前置：broker 支持 IPv6 监听（EMQX 5.8.6 默认监听 `[::]:1883`，本机 `192.168.20.6` 可达）。

步骤：
1. 用 IPv6 地址构建测试固件：
   `EXTRA_SDKCONFIG_DEFAULTS=<file>` 里写
   `CONFIG_COLLECTOR_MQTT_BROKER_URL="mqtt://[<broker 的全局 IPv6>]:1883"`；
2. 刷机，串口预期日志：
   - `WIFI` 连接成功 + `Got IP: <IPv4>`（双栈下 IPv4 仍会拿到）
   - 若 AP 通告 RA：`IP_EVENT_GOT_IP6` 路径由 IDF 打印（`esp_netif` 内部），
     设备侧可用 `ip -6 neigh` 或 broker 侧 `EMQX 连接来源地址` 观察到 v6 源；
3. 用 `tools/net_throughput_test.py` 以 IPv6 broker 地址做一次 soak（脚本本身是 MQTT 客户端，
   与 broker 地址是否为 v6 无关）：`--broker <v6 字面量或域名>`；
4. 判据：EMQX 侧 `client_connected` 事件里的 peer address 为 IPv6，且 MQTT 稳定不掉线。

本机 `net_throughput_test.py` 已在 **IPv4** 对本地 mosquitto 2.1.2 做过端到端自测（见 §4），
IPv6 实机部分标注 **待实机**。

---

## 3. PSRAM 型号（s3p）的放宽与记账

`sdkconfig.defaults.esp32s3psram` 追加（task-2 冻结行未动）：

```
CONFIG_ESP_WIFI_STATIC_RX_BUFFER_NUM=16
CONFIG_ESP_WIFI_DYNAMIC_RX_BUFFER_NUM=32
```

**TX 方向不用 dynamic 符号**：`TRY_ALLOCATE_WIFI_LWIP=y` 时 IDF 直接禁用 dynamic-TX 选项
（`esp_wifi/Kconfig:81    dynamic  depends on !(SPIRAM_TRY_ALLOCATE_WIFI_LWIP && ...)`），
TX 走 STATIC。构建出的 sdkconfig 实测：`CONFIG_ESP_WIFI_DYNAMIC_TX_BUFFER_NUM` 不存在，
生效的是 `ESP_WIFI_STATIC_TX_BUFFER_NUM=16` + `ESP_WIFI_CACHE_TX_BUFFER_NUM=32`
（IDF 该配置下的自身默认，`esp_wifi/Kconfig:74` "If PSRAM is enabled, Static should be selected"）。
这是任务卡原建议 "DYNAMIC_TX=32" 在 s3p 上**不适用**的实证。

两个关键事实：

1. **STATIC_RX=6 会硬编译失败；16 是文档要求的下限。** 这两个理由要分开说：
   - **硬下限**：`TRY_ALLOCATE_WIFI_LWIP=y` 时 IDF 把 `RX_BA_WIN` 默认抬到 16
     （`esp_wifi/Kconfig:204-205`），而 `wifi_init.c` 有两道编译期检查：
     ```
     :52  RX_BA_WIN <= DYNAMIC_RX_BUFFER_NUM
     :55  RX_BA_WIN <= 2 * STATIC_RX_BUFFER_NUM
     ```
     **实测反证**：把共享的 `STATIC_RX=6` 留在 s3p 上 → 构建失败
     `#error "WIFI_RX_BA_WIN should not be larger than double of the WIFI_STATIC_RX_BUFFER_NUM!"`。
     注意：任务卡原建议的 `STATIC_RX=10` 在算术上能过（2×10=20 ≥ 16），**不会**触发该 #error。
   - **任务卡建议值 10 为何不采用**：IDF 明确"static RX 建议 ≥ `RX_BA_WIN`"
     （`esp_wifi/Kconfig:37-39`），PSRAM 场景"默认且最小应为 16"（`esp_wifi/Kconfig:207-212`）。
     取 10 < RX_BA_WIN=16 会让 AMPDU 窗口大于 DMA ring，正是本型号要避免的聚合惩罚。
     16 也是 IDF 对该配置的默认值（`esp_wifi/Kconfig:30`）；这些字节落 PSRAM（见下），
     没有理由停在 10。

2. **这些字节应落在 PSRAM，内部 RAM 增量 ≈ 0**：
   - WiFi 侧 `wifi_malloc/calloc` = `heap_caps_malloc_prefer(size, 2, CAP_SPIRAM, CAP_INTERNAL)`
     （`esp_wifi/esp32s3/esp_adapter.c:69-100`）；
   - lwIP 侧 `mem_clib_malloc/calloc` 同样在 `SPIRAM_TRY_ALLOCATE_WIFI_LWIP` 下被重定义为
     先 PSRAM（`lwip/port/include/lwipopts.h:1743-1749`）；
   - 该路径**不经过** `SPIRAM_MALLOC_ALWAYSINTERNAL=4096` 小分配策略（那只作用于
     `malloc()`，`heap/heap_caps.c:107-124`）。
   - 字节量级：static 16×~1.6 KB ≈ 25.6 KB（实报 1.6 KB/块 `esp_wifi/Kconfig:32`）；
     dynamic 各 ≤ 32×1.6 KB = 51.2 KB 上限（按帧长实际更小）。
   - **待实机**：上电后比较 s3p 与 s3 的
     `heap_caps_get_free_size(MALLOC_CAP_INTERNAL)` /
     `heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL)`，
     并读 task-6 的 0x20 MemReport 的 free/largest/min_ever（500 Hz + 配置事务后）。
     若 internal free 明显下降，说明有 WiFi 分配未能落到 PSRAM 而静默回退。

---

## 4. 100 Hz 吞吐验证

### 4.1 工具

| 文件 | 作用 |
|---|---|
| `tools/net_throughput_test.py` | 对真实设备做 5×100 Hz 满配 soak；输出丢包率、p50/p99 时延、MQTT 掉线次数、payload 速率；JSON 结果 |
| `tools/net_harness_selftest.py` | **测量器自证**：合成一个 5×100 Hz 节点（注入 1% 丢包、25 ms 时延）跑同一分析路径，证明"测试真的会红" |

`net_` 前缀避免与 task-6 的 `mem_*` 冲突（lead 约定）。

### 4.2 判据（脚本内置）

- 丢包率 < 0.1%
- MQTT 不掉线（`disconnects == 0`）
- p99 端到端时延 < 200 ms
- 总速率在目标 ±5% 内
- 裸载荷 ≥ 140 KB/s（约 150 KB/s 预算）

时延口径：设备 `timestamp_us`（`esp_timer_get_time`，单调）→ 主机 `perf_counter`，
用 `min(recv - ts)` 估两机时钟偏移。**这不是绝对单向时延**：它测的是"相对最优单向时延的
附加时延（抖动 + 排队）"，常量部分（含固定单向传播）被 min 吸收。这是没有 NTP 时唯一
无时钟偏移的测法（本机 `esp_timer` 与主机 `perf_counter` 无共同时基）。
对本任务而言这是对的判据：100 Hz 要求关心的是**抖动/排队是否失控**，不是绝对传播时间。
文档与 JSON 里都按此口径解释，**不是**墙钟时间。

### 4.3 已完成的验证（本机）

**测量器自证（必须做，否则读不出"测试到底测没测东西"）**：对本地 mosquitto 2.1.2
（`eclipse-mosquitto:2`，端口 18830）运行 `net_harness_selftest.py`：

两次实跑（`--duration 8` 与 6，注入 1% 丢包、25 ms 常量 + 50 ms 抖动）：

```
injected_sent=4005  injected_dropped=32  injected_loss=0.80%
harness_received=2733  detected_missing=22  detected_loss=0.80%
harness_rate_hz=455.5  latency_p50=3.89ms   latency_p99=35.61ms
---（无抖动时 host 噪声底 ~30 ms）---

injected_loss=1.22%  harness_detected_loss=1.38%  harness_rate_hz=452.8
latency_p50=31.12ms  latency_p99=77.15ms   SELFTEST PASS (5/5)
```

即：注入 1.22% 丢包 → 测出 1.38%（同量级、方向正确）；注入 50 ms 抖动 → p99 77 ms；
速率 ~455/500 Hz（Python `sleep` 精度限制，属合成节点限制，不是被测固件）。
**分析器能检出注入的丢包与抖动，是诚实的。**

**对真实设备的 soak：待实机**。需要用户配合：一台刷入本分支 s3p（或 s3）+ 已配网 + 连到
可访问 broker 的节点；主机与 broker 同网。命令：

```bash
python3 tools/net_throughput_test.py --broker <broker-ip> --port 1883 \
        --node <CONFIG_COLLECTOR_NODE_ID> --duration 120 \
        --channels 5 --hz 100 --json soak.json
```

预期日志（固件侧）：
- `ConfigResult` success=true（manifest 5 通道，`interval_ms=10`）
- 无 `report pool exhausted` / `report_drop` 增长
- 无 `MQTT ... disconnected` / `reconnect`
- 脚本侧 `overall_pass: true`

### 4.4 测试的 manifest 说明

脚本发的 manifest 使用 S3 物理满配：UART0/1/2 + SPI2 + I2C0
（`S3_FULL_FIT`，引脚取 `hw_tables.c` 默认值；`bus_config` 字节序与 `bus_dma.c` 解析一致：
UART `[tx,rx,baud_be32]`、SPI `[cs,mode,freq_be32,mosi,miso,sclk]`、I2C `[sda,scl,addr,freq_be32]`）。
若测试板没有真实从设备，UART 读会超时并产生 `error_code` 报告；此时脚本会提示
`--allow-no-hardware`，并**不把该次运行当作 150 KB/s 载荷预算的证据**
（`note_no_data` / payload 检查降级）。这是刻意设计：不让"能跑"伪装成"达标"。

---

## 5. BLE 预算预留（本期不实现）

`sdkconfig.defaults` 中已写：`CONFIG_COLLECTOR_BLE=y` 时的 Kconfig 组合、
预算 48–64 KiB、以及验收标准。组合要点（NimBLE，peripheral only，1 连接）：

```
CONFIG_BT_ENABLED=y
CONFIG_BT_NIMBLE_ENABLED=y            # 不用 Bluedroid（更省 heap/flash）
CONFIG_BT_CONTROLLER_ENABLED=y
CONFIG_BT_NIMBLE_ROLE_CENTRAL=n
CONFIG_BT_NIMBLE_ROLE_OBSERVER=n
CONFIG_BT_NIMBLE_ROLE_BROADCASTER=y   # 广播：配网时需要
CONFIG_BT_NIMBLE_ROLE_PERIPHERAL=y
CONFIG_BT_NIMBLE_GATT_SERVER=y
CONFIG_BT_NIMBLE_MAX_CONNECTIONS=1
CONFIG_BT_NIMBLE_ATT_PREFERRED_MTU=256
CONFIG_BT_NIMBLE_MEM_ALLOC_MODE_INTERNAL=y
CONFIG_BT_NIMBLE_HOST_TASK_STACK_SIZE=4096    # 必须在内部 RAM
```

**验收标准（写入 defaults 注释）**：
在 `CONFIG_COLLECTOR_BLE=n` 的最坏运行态（500 txn/s + 配置事务 + 日志流）下，
internal free 必须 ≥ **BLE 预算 + 8 KiB**；按最小预算即 **≥ 56 KiB**，
且 largest free block 足够创建 BLE 任务栈（内部 RAM）。
task-8 只在门禁通过后才实现；s3p 优先。

---

## 6. 构建验证结果

### 6.1 冻结快照 A/B（隔离队友并发改动，最干净）

方法：`git clone` 出 HEAD `ed1a4fce` 快照，叠加 task-1/task-2 已完成的文件作为 **base**；
再叠加我的两个 defaults 文件作为 **after**。三 profile × 两变体全部构建通过：

| profile | base ELF | after ELF | 结果 |
|---|---|---|---|
| s3-n16 | ✅ | ✅ | 通过 |
| c6-n16 | ✅ | ✅ | 通过 |
| s3p-n16 | ✅ | ✅ | 通过（+ 反证：去掉 relaxed 段则 `#error`，见 §3） |

### 6.2 静态 RAM 对比（`nm` 符号级，S3）

同一次快照 A/B、同工具链，`.dram0.bss` 变化极小（WiFi/LWIP 缓冲是**堆分配**，不入 .bss）：

| 项 | base | after | Δ |
|---|---:|---:|---:|
| `.dram0.bss` (S3) | 97,520 | 97,104 | **−416** |
| `.dram0.data` (S3) | 22,237 | 22,221 | **−16** |
| `.flash.text` (S3) | 872,546 | 803,426 | **−69,120**（主要来自 DEBUG_TCP / SoftAP / mDNS / Enterprise 关闭） |

符号级明细（S3，节选）：`d_mult_table −1024`、`socket_ipv6_multicast_memberships −112`、
`sockets −80`、`s_sm_table −64`、`socket_ipv4_multicast_memberships −48`、
`dns_mquery_v4group/v6group −24/−24`、多个 SoftAP/Enterprise 相关静态符号 −4。
合计 bss+data 符号 **−1,658 B**。

> 另：`.flash.rodata` base=288,428 → after=280,500（**−7,928 B**）。

**堆（动态）节省模型**——这才是网络定容的主要收益。WiFi/LWIP 缓冲不在 .bss，
而是 `esp_wifi_init()` 时/按需从堆分配（`esp_adapter.c:69-100`）。按 IDF 给的
"每块约 1.6 KB"（`esp_wifi/Kconfig:32`）与 IDF 峰值公式 `p=b_rx*m_rx+b_tx*m_tx`
（Wi-Fi 性能指南）：

| 项 | 原值→新值 | 常驻/峰值节省 | 口径 |
|---|---|---|---|
| static RX ring | 10 → 6 | **−6.4 KB 常驻** | 4 × 1.6 KB；`esp_wifi_init` 时分配，直到 deinit 不释放 |
| dynamic RX | 32 → 16 | 峰值上限 **−25.6 KB**；300 B 帧时按帧长成比例（远小于此） | 16 × 1.6 KB 是**满长帧上限**，非实际常驻 |
| dynamic TX | 32 → 16 | 峰值上限 **−25.6 KB**；同上 | 同上 |
| mgmt s-buf | 32 → 16 | **≈ −1 KB** 上限 | 每块 <64 B（`esp_wifi/Kconfig:270`） |
| TCPIP recvmbox | 32 → 16 | 16 × 每消息开销（指针级，几百 B 量级） | lwIP mbox 存的是消息指针 |
| TCP sndbuf/wnd | 不变 | 0（刻意不动） | 保 p99/吞吐 |

即：**常驻堆节省 ≥ 6.4 KB**（static ring，可确定），动态峰值上限再省最多 ~52 KB
（取决于帧长，300 B 采集流量下实际远低于上限）。对"配置事务时堆只剩数百字节"的
根问题，6.4 KB 常驻 + 峰值压缩是直接缓解。

> 堆峰值的前后对比最终由 task-6 的构建期门禁 + 运行期 0x20 MemReport 在实机上测量；
> 本表是**基于 IDF 文档的模型**，实机数据出来后应以实机为准。

### 6.3 worktree 最终验证

最终一轮（20:46，worktree，含所有队友已合入的改动）三 profile 全绿，并逐项读回生效值：

| profile | 结果 | STATIC_RX | DYN_RX | MGMT | TCPIP_MBOX | SOCK | UDP_MBOX | IPv6/AUTOCONF | DHCPS | SOFTAP | DEBUG_TCP | WPA3-SAe |
|---|---|---:|---:|---:|---:|---:|---:|---|---|---|---|---|
| s3-n16 | ✅ | 6 | 16 | 16 | 16 | 6 | 6 | y/y | n | n | n | y |
| c6-n16 | ✅ | 6 | 16 | 16 | 16 | 6 | 6 | y/y | n | n | n | y |
| s3p-n16 | ✅ | 16 | 32 | 16 | 16 | 6 | 6 | y/y | n | n | n | y |

> 注：c6-n16 第一次（20:17）失败的根因是 task-3 正在改的
> `components/config_mgr/config_mgr.c`（非 PSRAM 分支踩 `-Werror=address`），
> **不是本任务的改动**。task-3 用 `manifests_ready()` 修复后，20:30 复跑通过。

---

## 7. 待办 / 风险

| # | 事项 | 归属 |
|---|---|---|
| 1 | `wifi_mgr.c` provisioning 路径条件编译（空 SSID + SOFTAP=n 构建） | task-8 |
| 2 | 注册 `IP_EVENT_GOT_IP6`，支持 IPv6-only | task-8 |
| 3 | 实机 5×100 Hz soak + IPv6 broker 连接记录 | 需硬件（用户配合） |
| 4 | s3p 内部 RAM free/largest 与 s3 对照 | 实机（task-6 门禁） |
| 5 | 若实机出现 `report_drop>0`，回调 STATIC_RX 6→8 | task-4 follow-up |
