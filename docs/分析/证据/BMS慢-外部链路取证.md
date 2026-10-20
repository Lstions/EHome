# BMS 详情页慢 —— 外部访问链路取证（外部入口 / 外部 vs LAN / gzip / HTTP / WS）

- 取证日期：2026-10-20 21:00–21:35 CST（测量时间见各节；生产日志为 UTC，比 CST 晚 8 h）
- 对象：生产 EHome BMS 详情页 `/edge-device/14`（`edge_devices.id=14`，jiabaida_bms，16 串，1 Hz，29 测点）
- 生产：`192.168.20.6:18090` → 容器 `ehome-prod-web`（镜像 `ehome-new:ui-fix2`）→ `ehome-prod-postgres`（库 `ehome`，`unified_data` 约 2400 万行/4.4 GB）
- 本机视角：`192.168.20.3`（与生产同 192.168.20.0/24、同 IPv6 /64 `2409:8a55:16c0:8150::/64`，RTT < 1 ms）
- 分工：本文负责「外部链路」；服务端成本见 [BMS慢-后端查询取证.md](./BMS慢-后端查询取证.md)，浏览器请求/超时/降级见 [BMS慢-前端请求取证.md](./BMS慢-前端请求取证.md)，汇总见 [BMS页外部访问慢-根因分析-2026-10-20.md](../BMS页外部访问慢-根因分析-2026-10-20.md)

> **红线遵守**：生产全程只读（GET / SELECT / EXPLAIN / cat / ss / iptables-save 语义的读取）；未重启容器、未写库、未改 nginx/防火墙/DNS；未登录 MikroTik/3x-ui 后台，未做任何配置变更。

---

## 0. TL;DR（结论先行）

1. **外部入口【实测+推断】**：生产 18090 **没有任何反向代理入口**——nginx-proxy-manager 的 6 个 proxy_host（含其 DB 里的 6 行记录）里不存在 EHome/18090；`grep -rn 18090 /srv/nginx-proxy-manager/data/` 为空。18090 是 Docker 直接发布在宿主 `0.0.0.0:18090` 与 `[::]:18090`，宿主防火墙 INPUT 全 ACCEPT（只挂了 Tailscale 链）。因此外部访问只有两种可能路径：
   - **路径 A（最可能，仓库文档记录的生产页访问地址）**：Tailscale 覆盖网 → `http://100.64.0.7:18090`（或在开启子网路由的客户端上用 `http://192.168.20.6:18090`）。【推断，证据见 §2.3】
   - **路径 B**：公网 IPv6 直连 → `http://qsun.fun:18090`（AAAA=生产 IPv6，TTL 600；无 A 记录；无 NAT）。【推断：可达性未取得外部实测，见 §2.5/§8】
   - **已排除**：NPM 反代、`console.qsun.fun/ehome-dev`（该 location 指向开发机 192.168.20.3:5174，且 5174 当前未监听，也非生产 18090）、3x-ui 作为入站代理（其唯一入站 10808 是 xray mixed，noauth 仅限 127.0.0.1；它是"出站翻墙客户端"，不是 LAN 入口）。

2. **外部 vs LAN【实测基线 + 模型】**：服务端单请求 TTFB 就是 5.6–9.9 s（Lead 基线，loadavg≈3）；LAN 上 1.5 MB 的线上传输只有约 15 ms（≈105 MB/s）。外部链路在**正常宽带 + gzip 生效**时只增加约 **0.03–0.3 s/请求**（RTT 20–60 ms + 165 KB/(10–30 Mbps) + TCP 慢启动 1–2 RTT），对 cells 5.64 s → 5.7–6.0 s（**1.0–1.06×**）。只有「弱网（≲2 Mbps）或 gzip 失效（传输量 1.57 MB→2.93 MB/页）」时传输项才升到秒级，放大到 1.2–2×。

3. **关键判断【实测+计算】**：外部相对 LAN 额外增加的是 **RTT×若干次 + 传输时间**，不是"MB 级传输时间"——只要 gzip 生效且下行 ≥10 Mbps，传输量（整页编码后仅 **267,739 B**，前端取证 §3/§8）连 0.2 s 都不到。**服务端 5.6–9.9 s（串行）/ 9.8–16.7 s（页面 5 并发）占绝对主导（>95% 墙钟）**。

4. **10 s axios 超时的临界条件**：`T_server(页面并发时) + T_external(传输+排队) > 10 s`。实测页面 5 并发时服务端已把 4/5 个 batch 推到 9.79–16.70 s（Lead 6 并发实验）——**LAN 上就已经 4/5 超时**；外部的边际作用是把已经贴线的 **cells 9.79 s 推过 10 s**、把弱网下的 temp（7.72 s）也推过线。一旦多 1 个请求超时，就会触发 16 个逐测点降级请求（前端取证 §4），进一步抬升服务端负载——这才是"外部更慢"的非线性放大机制。
   - 带宽竞争：整页 API 编码字节 267,739 B。即便 5 个响应同时下载，10 Mbps 下 0.21 s、2 Mbps 下 1.07 s，**在 ≥10 Mbps 且 gzip 生效时带宽竞争不构成瓶颈**；只有「无 gzip + ≤2 Mbps」时，2.39 MB 解码体以上行 ~9.6 s 竞争，才升级为并列主因。

5. **gzip/HTTP/WS【实测】**：gzip 在 18090 上**真实生效**（`gin-contrib/gzip`，cells 1,570,805 B → 164,958 B，9.52×；全页编码 267 KB）；`Content-Encoding: gzip`、`Vary: Accept-Encoding`、`Transfer-Encoding: chunked`（无 Content-Length）；**仅 HTTP/1.1**（明文端口无 TLS、无 h2c，`--http2-prior-knowledge` 直接失败）；keep-alive 可复用（同连接第 2/3 请求 `num_connects=0`）。WS `/api/v1/ws` 直连路径 101 正常；18090 链路上**没有 nginx/反代**，不存在 `proxy_read_timeout` 掐断；后端 WS 无 ping/无读超时，前端 45 s 应用层心跳 + 1 Hz 数据帧；断线重连只额外发 1 次 `GET /api/v1/account`，**不重取历史数据** ⇒ WS 重连与"反复 1.5 MB 请求"无关。

6. **放大系数（本次口径）**：同一 BMS 首屏，LAN 墙钟 **16.71 s（6 并发）/ 20.7 s（浏览器实测含降级风暴）**；外部估计（正常宽带+gzip）**16.8–17.6 s（1.00–1.05×）**、弱网+gzip 17.5–19 s（1.05–1.14×）、无 gzip 18.5–20 s（1.11–1.20×）、无 gzip+弱网 25–30 s+（1.5–1.8×）。但**失败数**会被放大：LAN 12 失败 → CDP 模拟外部慢网（500 ms RTT/1.6 Mbps）28 失败、5/5 batch 超时、图表全部"暂无数据"（前端取证 §4.3）。

7. **限制（诚实声明）**：**未复现真实外部路径**。本机与生产同 LAN、同 /64，所有本地 IPv6 测量都是"同 /64 直连"（RTT 0.79 ms、traceroute 1 跳），不代表公网；Tailscale 视角是 LAN direct（1 ms），不代表远程 overlay；没有取得可执行大响应测量的真实外部 vantage（第三方探测节点不支持 AAAA-only 目标/非标端口）。因此 §4 的外部数字是**模型估计**，§3.2 的外部数字是 **CDP 限速模拟**，均已逐项标注。

---

## 1. 测量条件与污染声明（引用本文数字前必读）

### 1.1 三个独立的口径，不要混用

| 口径 | 时间（CST） | 生产 loadavg | 条件 | 用途 |
|---|---|---|---|---|
| **Lead 串行基线** | 20:57–21:05 | ≈3.0（3 vCPU） | LAN 直连、单请求串行、**不压缩** | 唯一"干净"耗时基线 |
| **本文取证测量** | 21:13–21:19 | 1.45 → 14.46（我自己的串行重查询也会拉高）→ gzip 矩阵窗口 8.28/10.13/11.05（min/中位/max，n=40，21:15:03–21:15:43）→ 并发实验 9.98→12.56 | LAN 直连、单请求串行为主（1 次 8 并发） | **只用其体积/压缩比/协议事实**；耗时只作"受干扰上界" |
| **前端 A-lan / B2-throttle** | 21:08 / 21:23 前后 | 6–11 | 真实浏览器 CDP；B2 为 `Network.emulateNetworkConditions{latency:500ms, downloadThroughput:204800 B/s}` **模拟**外部 | 页面级请求数与失败数；B2 作弱网近似 |

> 说明：本文档不重复跑重查询。§1.1 里"本文取证测量"的耗时（例如 cells 曾出现 14.1/19.3 s，而 2 分钟后同样请求 5.72 s）证明**生产测量对并发取证负载极敏感**，绝不能当基线。

### 1.2 关键事实：本机不是外部

```
# ip -brief addr（本机 192.168.20.3）
enp6s0  UP  192.168.10.5/24 10.0.10.253/24 192.168.20.3/24
            2409:8a55:16c0:8150:d1cc:97e6:6b56:37e9/64 ... 2409:8a55:16c7:4220:.../64
# 生产 eth0（192.168.20.6）
eth0@if11 UP 192.168.20.6/24 2409:8a55:16c0:8150:be24:11ff:fe98:ed9a/64 ...
# ping6 生产公网地址（本机→生产）        # traceroute6 生产公网地址
4 packets transmitted, 4 received, 0% loss  # 1  2409:8a55:16c0:8150:be24:11ff:fe98:ed9a  0.821 ms
rtt min/avg/max/mdev = 0.729/0.786/0.816/0.033 ms  #（1 跳 = on-link，根本没出路由器）
```

⇒ 本机的 `2409:8a55:16c0:8150::/64` 与生产同段、同二层，**不是外部链路**。这是本文最重要的限制（§8）。

---

## 2. 外部入口排查：18090 到底怎么被访问

### 2.1 反向代理（nginx-proxy-manager）：**没有 EHome 入口**【实测】

```
$ for f in /srv/nginx-proxy-manager/data/nginx/proxy_host/*.conf; do echo "##### $f"; cat "$f"; done
##### 1.conf   # console.qsun.fun:  root→ https://192.168.20.2:8006（Proxmox），/ehome-dev→ http://192.168.20.3:5174
##### 2.conf   # ai.qsun.fun:      → http://192.168.20.6:8080（sub2api）
##### 3.conf   # syn.qs.cn, syn.qsun.fun: → https://192.168.20.7:5001
##### 4.conf   # ai.qs.cn:         → http://192.168.20.6:8080
##### 6.conf   # dsh.qsun.fun:     → https://192.168.20.3:443（带 auth_basic）
```

NPM 数据库（`/srv/nginx-proxy-manager/data/database.sqlite`，只读查询）共 6 条 proxy_host，逐条列出与磁盘一致：

```
$ python3 - <<'PY'   # 以 mode=ro 打开
... select id,domain_names,forward_host,forward_port,enabled,created_on,modified_on from proxy_host
(1, '["console.qsun.fun"]', '192.168.20.2', 8006, 1, '2026-08-23 10:33:49', '2026-08-30 07:59:27')
(2, '["ai.qsun.fun"]',      '192.168.20.6', 8080, 1, '2026-08-23 12:36:33', '2026-09-01 03:39:19')
(3, '["syn.qs.cn","syn.qsun.fun"]', '192.168.20.7', 5001, 1, '2026-08-30 06:37:37', '2026-10-19 01:44:39')
(4, '["ai.qs.cn"]',         '192.168.20.6', 8080, 1, '2026-09-01 03:35:33', '2026-09-01 03:39:26')
(5, '["console.qs.cn"]',    '192.168.20.2', 8006, 1, '2026-09-01 03:57:49', '2026-09-01 04:05:04')
(6, '["dsh.qsun.fun"]',     '192.168.20.3', 443,  1, '2026-10-07 02:40:51', '2026-10-07 02:40:51')
```

```
$ grep -rn "18090" /srv/nginx-proxy-manager/data/    # 无输出（退出码 1）
```

【实测】6 个入口里没有任何一个指向 18090 / EHome；EHome 于 2026-10-18 部署到生产，NPM 的 6 条记录创建于 8/23–10/7，**之后没有任何新增**。⇒ **EHome 生产 Web 不走 NPM 反代。**

### 2.2 端口发布与监听：**宿主全接口直出，没有中间代理**【实测】

```
$ docker ps --format "{{.Names}}|{{.Image}}|{{.Ports}}"
ehome-prod-web|ehome-new:ui-fix2|0.0.0.0:8443->8443/tcp, [::]:8443->8443/tcp, 0.0.0.0:18090->8080/tcp, [::]:18090->8080/tcp
（另：sub2api 8080、3xui_app 2053/10808、nginx-proxy-manager 80/81/443/8866/3443、ddns-go -）

$ ss -lntp | grep -E ':18090|:2053|:10808'
LISTEN 0 4096 0.0.0.0:10808  ... docker-proxy
LISTEN 0 4096 0.0.0.0:18090  ... docker-proxy
LISTEN 0 4096 [::]:18090     ... docker-proxy
```

生产只起了 14 个 systemd 服务（docker/containerd/tailscaled/ssh/postfix/systemd-*），**宿主没有 nginx/haproxy/caddy/frp**；18090 由 docker-proxy 直接 DNAT 到容器 8080。

### 2.3 防火墙：**主机层不设防，暴露完全由上游 CPE 决定**【实测】

```
$ iptables -S INPUT
-P INPUT ACCEPT
-A INPUT -j ts-input          # 仅 Tailscale 链
$ ip6tables -S INPUT
-P INPUT ACCEPT
-A INPUT -j ts-input
$ nft list ruleset | head
table inet filter { chain input { type filter hook input priority filter; policy accept; } ... }
$ ufw status → bash: ufw: 未找到命令
```

⇒ 主机不拦任何入站端口；IPv4/IPv6 是否可达由上游 MikroTik RouterOS 7.24.5（`192.168.20.254`，netcheck 的 UPnP 响应可证）与运营商决定（没有路由器凭据，未读取其配置——只读红线内也不做登录尝试）。

### 2.4 DDNS / DNS：**只有 IPv6 域名，没有 A 记录**【实测】

```
$ cat /srv/ddns-go/data/.ddns_go_config.yaml     # secret 已脱敏
dnsconf:
  - ipv4: {enable: false, gettype: url, netinterface: eth0, domains: [""]}
    ipv6:
      enable: true
      gettype: url
      url: https://speed.neu6.edu.cn/getIP.php, https://v6.ident.me, https://6.ipw.cn, ...
      netinterface: eth0
      domains:
        - qsun.fun
        - console.qsun.fun
        - ai.qsun.fun
        - syn.qsun.fun
        - dsh.qsun.fun
    dns: {name: dnspod, id: "570259", secret: <redacted>}
```

```
$ dig +short A qsun.fun / console.qsun.fun / dsh.qsun.fun   # 三者均无输出
$ dig +short AAAA qsun.fun
2409:8a55:16c0:8150:be24:11ff:fe98:ed9a
$ dig qsun.fun AAAA +noall +answer
qsun.fun.  600  IN  AAAA  2409:8a55:16c0:8150:be24:11ff:fe98:ed9a
```

【实测】DDNS 只维护 **IPv6 AAAA**（IPv4 关闭）；qsun.fun 系列域名解析到生产宿主 IPv6，TTL 600 s。⇒ **带域名的公网入口只可能是 `http://qsun.fun:18090`（IPv6）**；没有 IPv4 域名入口（IPv4 侧是否在 MikroTik 上做了端口映射未验证，见 §2.5）。

### 2.5 外部可达性探测：**IPv6 未能证实，IPv4 未发现端口映射**【实测，有局限】

用第三方外部探测服务（check-host.net，从境外节点发起，只读探测）：

```
# 1) HTTP 探测 http://qsun.fun:18090/  —— 6 个节点（DE/HU/ID/IN/MD/US）
$ curl -H 'Accept: application/json' 'https://check-host.net/check-http?host=http%3A%2F%2Fqsun.fun%3A18090%2F&max_nodes=6'
$ curl 'https://check-host.net/check-result/523b62f9k3cd'
{"de4.node...":[[0,0.006,"No such device or address",null,null]],
 "hu1...":[[0,0.709,"No such device or address",null,null]], ... 全部 6 节点同报错}
```

【实测/局限】6 个节点在 1 s 内全部返回 ENXIO——这是探测端**无法解析 AAAA-only 域名/不支持 IPv6 目标**的典型表现，**不能据此判定 18090 不可达**。

```
# 2) TCP 探测 当前公网 IPv4 27.47.39.31:18090 —— 3 个节点（IR/SG/UK）
$ tailscale netcheck | grep IPv4        # → IPv4: yes, 27.47.39.31:6272（另一时刻曾为 223.73.114.179，双 WAN/动态）
$ curl -H 'Accept: application/json' 'https://check-host.net/check-tcp?host=27.47.39.31%3A18090&max_nodes=3'
$ curl 'https://check-host.net/check-result/523c2af6k2bc'
{"ir1...":[{"error":"Connection timed out"}],"sg1...":[{"error":"Connection timed out"}],"uk1...":[{"error":"Connection timed out"}]}
```

【实测/局限】3 个境外节点到当前公网 IPv4:18090 全部连接超时 ⇒ **没有证据表明 IPv4 侧存在 18090 端口映射**；但 WAN 为**双出口/动态 IP**（netcheck 先后采到 `27.47.39.31` 与 `223.73.114.179`，tailscaled 的 Addrs 里也同时有这两个 + 多个 IPv6），样本不足以完全排除另一条 WAN 上的映射。

### 2.6 3x-ui：**是出站代理客户端，不是 LAN 入口**【实测】

```
# x-ui.db（只读）
inbounds id=1: port=10808 protocol=mixed enable=1
  settings = {"auth":"noauth","accounts":[],"udp":false,"ip":"127.0.0.1"}
settings xrayTemplateConfig.outbounds:
  {"tag":"proxy","protocol":"vless","address":"vps1.qsun.fun","port":443,...}   # 出站到 VPS
  {"tag":"direct","protocol":"freedom",...}
listen: 0.0.0.0:10808（docker-proxy）, 0.0.0.0:2053（面板）
```

【实测】10808 的 mixed 入站 noauth 只放行 `127.0.0.1`（xray socks "ip" 字段语义），且无认证账号；它是家里这台机器**出站翻墙**用的客户端，不构成本地 LAN/公网的 EHome 入口。面板 2053 与 EHome 无关。

### 2.7 Tailscale：**存在且是生产页在仓库文档里的访问地址**【实测】

```
# 生产机
$ tailscale status
100.64.0.7   pve-docker   sun   linux   -          # 生产自己
100.64.0.10  chpc         sun   linux   active; direct 192.168.20.3:53333, tx 252227484 rx 912600124
...
# health: Some peers are advertising routes but --accept-routes is false

# 本机（192.168.20.3）
$ tailscale ping -c 3 100.64.0.7
pong from pve-docker (100.64.0.7) via [2409:8a55:16c7:4220:be24:11ff:fe98:ed9a]:41641 in 1ms   # LAN direct
$ tailscale netcheck
* IPv4: yes, 27.47.39.31:5684（另一次 223.73.114.179）   * IPv6: no, but OS has support
* PortMapping: UPnP   * Nearest DERP: YeCaoYun HongKong
  DERP latency: hk 42.3ms / fkr 254.3ms / headscale 316.4ms
```

- 生产 TS IP = **100.64.0.7**，本机 = 100.64.0.10（它是 **192.168.20.0/24 子网路由器**：`AllowedIPs: ["100.64.0.10/32","192.168.20.0/24",...]`，`PrimaryRoutes: ["192.168.20.0/24"]`）。
- 仓库文档记录的生产页访问目标就是 Tailscale IP：`docs/分析/逆变器页UI与图表缺陷分析-2026-10-20.md:3` → **`http://100.64.0.7:18090/edge-device/15`**。
- 本机→生产的 TS 是 **LAN direct（1 ms）**（经生产的另一个 on-link IPv6 地址:41641），**不是 DERP relay**；远程用户的 TS 路径可能是 direct 也可能走 hk/fkr relay（取决于 NAT 穿越），本文无法从本机复现远程 TS 的真实 RTT。

### 2.8 入口判定与链路图

**判定【实测支撑的推断】**：
- **事实层**：18090 = 宿主全接口直出 + 无 NPM/反代 + 宿主无防火墙 + 域名只有 AAAA + TS 网络就位 + 3x-ui 不是入口。
- **最可能路径 = A（Tailscale）**：理由 ① 仓库里生产页的访问 URL 明确写成 `100.64.0.7:18090`；② 用户家里有 Windows/Android/macOS 多台设备在 TS 网内，chpc 还专门做了 192.168.20.0/24 子网路由——这套配置的目的就是"人不在家时访问家里 LAN"；③ 该路径与运营商 IPv6 入站策略无关，最稳。
- **次可能路径 = B（公网 IPv6 直连）**：DDNS 维护 qsun.fun 等 AAAA、18090 又发布在 `[::]`，用户完全可能直接开 `http://qsun.fun:18090/`；但 **IPv6 入站是否被 MikroTik 放行未取得证据**（只有一个反证方向：IPv4:18090 外部探测不通）。
- **排除**：NPM 反代、`console.qsun.fun/ehome-dev`（→ 开发机 5174，实测当前 `ss -lntp` 无 5174 监听，且它不是生产 18090）、3x-ui 入站、`ai.qsun.fun`（→ 8080 sub2api）。

```
路径 A（最可能）：外部用户 ──Tailscale overlay──▶ 生产 100.64.0.7:18090
  ① 用户终端（Windows/手机/mac，已登录同一 Tailnet）
  ② WireGuard 隧道（direct UDP 41641，或 NAT 失败时经 DERP: hk 42ms / fkr 254ms）
  ③ 生产 tailscaled（100.64.0.7）→ 本地 18090
  ④ docker-proxy 127.0.0.1?/宿主 0.0.0.0:18090 → 容器 172.21.0.x:8080（ehome-prod-web）
  ⑤ 容器内 Go http（gin，gzip 中间件）→ PG 172.21.0.x:5432
  备注：若走 chpc(100.64.0.10) 子网路由，则为 100.64.0.7? 不经过——直接尾包到生产；
        若用户用 192.168.20.6 访问且路由经 chpc，则多一跳 chpc 转发。

路径 B（次可能）：外部用户 ──公网 IPv6──▶ 生产 [2409:8a55:16c0:8150:be24:11ff:fe98:ed9a]:18090
  ① 用户终端（网络需有可用 IPv6；qsun.fun 无 A 记录，无 IPv4 回退）
  ② DNS：qsun.fun AAAA（TTL 600，DNSPod）——浏览器 Happy Eyeballs 对单一 AAAA 不生效
  ③ 家庭宽带（MikroTik RouterOS 7.24.5）→ IPv6 无 NAT 直通（入站是否被 CPE 防火墙放行：未验证）
  ④ 生产 eth0 全局 IPv6:18090 → docker-proxy → 容器:8080
  ⑤ 同上 Go/gzip → PG

已排除：NPM(443/80 → 其它服务) / console.qsun.fun/ehome-dev(→开发机5174) / 3x-ui(出站代理)
```

---

## 3. 外部 vs LAN 量化（同一请求、同一时刻的服务端成本）

### 3.1 传输耗时分解：LAN 上传输占比 <0.3%【实测】

LAN（21:15，loadavg 8–11 受干扰窗口；体积/比例不受影响），IPv4 直连、单请求：

| 请求（Lead 参数：max_points=500） | 体积（未压缩） | TTFB | total | total−TTFB（线上传输） |
|---|---:|---:|---:|---:|
| cells 16 类 × 24 h，identity | 1,570,805 B | 5.776 s | 5.791 s | **0.015 s（≈105 MB/s）** |
| protection 30 d，identity | 100,213 B | 6.424 s | 6.426 s | ~0.002 s |
| cells 16 类 × 24 h，**gzip** | 164,958 B | 5.718 s | 5.737 s | 0.018 s |
| metrics 9 类 × 24 h，gzip | 94,086 B | 3.358 s | 3.367 s | 0.009 s |
| protection 30 d，gzip | 10,313 B | 6.092 s | 6.092 s | <0.001 s |
| temp 3 类 × 24 h，gzip | 30,871 B | 1.182 s | 1.184 s | 0.002 s |
| soc+current 2 类 × 24 h，gzip | 20,679 B | 0.821 s | 0.822 s | 0.001 s |

【实测结论】TTFB ≈ total；**LAN 上 1.5 MB 的传输只花 15 ms**，服务端算完才发。响应头（同批实测）：`Transfer-Encoding: chunked`、`Content-Encoding: gzip`、`Vary: Accept-Encoding`、**无 Content-Length**；HTTP 版本 `1.1`。

### 3.2 压缩比（服务端 gzip 实测）【实测】

| 请求 | 未压缩 B | gzip B | 压缩比 |
|---|---:|---:|---:|
| cells 16×24h | 1,570,805 | 164,958 | **9.52×** |
| metrics 9×24h | 891,923 | 94,086 | 9.48× |
| protection×30d | 100,213 | 10,313 | 9.72× |
| temp 3×24h | 310,787 | 30,871 | 10.07× |
| soc+current 2×24h | 185,505 | 20,679 | 8.97× |
| **首屏 8 个 API 合计** | **≈3,072,495** | **≈324,520（≈317 KB）** | ≈9.5× |

浏览器实测整页 44 个 API 的**编码后总字节只有 267,739 B**（前端取证 §3.1/§8）——比上面 8 个主要响应的理论合计还小，因为失败请求没有响应体。

### 3.3 外部传输时间：假设场景计算（模型，非实测）

公式：`T_external ≈ T_server_TTFB + RTT_conn + N_ramp × RTT + Bytes / BW_uplink`
（`N_ramp` ≈ 1–2，TCP 慢启动把 body 发完所需额外 RTT 数；视初始 cwnd 与丢包而定）

带宽/RTT 实测锚点（本机，同一 CPE/同一条家宽；见 §5）：

| 测量 | 结果 | 说明 |
|---|---|---|
| AliDNS `2400:3200::1` ping6 | **11.07 ms**（min/avg/max 10.658/11.068/11.504） | 国内同省/邻省 RTT 量级 |
| Cloudflare IPv6 `speed.cloudflare.com` ping6 | **175.68 ms**（25% 丢包） | 国际出口 RTT 量级 |
| Cloudflare 下行 10 MB | 3.179 s = **25.2 Mbps（3.15 MB/s）** | 国际方向下行下限 |
| Cloudflare 上行 8 MB（POST） | 2.420 s = **26.4 Mbps（3.3 MB/s）** | 国际方向**家宽上行**下限 |
| 清华 TUNA 下行 10 MB（IPv6） | 0.718 s = **111 Mbps（13.9 MB/s）** | 国内下行 |
| 家宽出口 | 双 WAN 动态：`27.47.39.31` / `223.73.114.179`（不同时刻） | CPE=MikroTik 7.24.5，UPnP 开启 |

**单请求外部耗时估算**（以 Lead 基线 TTFB 为服务端项）：

| 场景（假设，明确标注） | 假设 | cells（1.57 MB→gzip 165 KB；服务端 5.64 s） | protection（100 KB→gzip 10 KB；服务端 9.87 s） |
|---|---|---|---|
| S0 LAN 实测 | 同 LAN | 5.64 s（1.00×） | 9.87 s（1.00×） |
| S1 国内宽带 | 上行 30 Mbps、RTT 20 ms、gzip | **5.74–5.80 s（1.02–1.03×）** | **9.90–9.93 s（1.003–1.006×）** |
| S2 4G/普通移动网 | 上行 10 Mbps、RTT 60 ms、gzip | **5.86–6.00 s（1.04–1.06×）** | **9.95–10.05 s（1.008–1.02×，可能越线）** |
| S3 弱网/relay | 上行 2 Mbps、RTT 100 ms、gzip | **6.3–6.7 s（1.12–1.19×）** | **10.1–10.3 s（1.02–1.04×，越线）** |
| S4 gzip 被剥离 | 上行 10 Mbps、RTT 60 ms、**identity** | **7.0–7.3 s（1.24–1.29×）** | 10.1 s 左右（越线） |
| S5 弱网且无 gzip | 上行 2 Mbps、RTT 100 ms、identity | **11.9–12.5 s（2.1–2.2×）** | 10.4–10.6 s（1.05–1.07×） |

> S1/S2/S3 的传输项：165 KB ÷ (30/10/2 Mbps)= 44/132/660 ms；保护 10 KB 对应 2.7/8/41 ms。RTT 20/60/100 ms × (1 握手 + 1–2 慢启动) ≈ 40–300 ms。数字为模型估计，**不是真实外部实测**。

### 3.4 页面级（含并发争用）：CDP 限速模拟给出的外部近似【实测-模拟】

前端取证 §4.3 的 B2-throttle：CDP `Network.emulateNetworkConditions{latency:500 ms, downloadThroughput:204800 B/s(≈1.6 Mbps), uploadThroughput:102400 B/s}` + 40 s 热缓存预热后重载（**模拟**，非真实外部）：

| 指标 | A-lan（真实 LAN） | B2-throttle（模拟外部慢网） |
|---|---:|---:|
| API 请求数 | 44 | 40 |
| 失败（页面侧 timeout） | 12 | **28** |
| batch 超时 | 4/5 | **5/5** |
| 降级请求成功 | 14/22 | **0/22** |
| latest-data | 9.72 s 成功 | **20.001 s abort**（其单独放宽到 20 s） |
| 最终图表 | 电芯 14/16、3 卡"暂无" | **电芯 0/16、4 卡全部"暂无"** |
| 编码字节 | 267,739 B | 108,148 B（大量无响应体） |

【实测-模拟结论】外部慢网**不会把墙钟线性拉长几倍**（请求被 10 s/20 s 上限截断），但会把**失败率**从 12/44 抬到 28/40，把"部分图表缺数据"变成"全部图表无数据"。这正是用户"外部访问特别慢/图表一直加载/数据没有"的体感来源。

---

## 4. 关键判断：外部多出来的是「RTT」还是「MB 级传输时间」？

### 4.1 定量结论

- 服务端 TTFB：**5.6–9.9 s/请求**（Lead 串行基线），页面 5 并发时 **9.79–16.70 s**（Lead 并发实验）——占绝对主导。
- 外部传输增量（正常宽带 + gzip）：**0.03–0.3 s/请求**（S1/S2），即**不到服务端的 3%**。
- 外部传输增量（无 gzip/弱网）：0.3 s–6.6 s/请求（S4/S5），这时才可能到服务端同量级。
- LAN 自身传输：**0.015 s/1.5 MB**（≈105 MB/s）。
- 整页编码字节：**267,739 B**（浏览器实测）⇒ 10 Mbps 下 0.21 s、30 Mbps 下 0.07 s、2 Mbps 下 1.07 s。

⇒ **外部相对 LAN 额外增加的主体是「若干次 RTT + 少量传输时间」，不是"MB 级传输时间"。** 只有当「gzip 失效（3 MB 级）+ 弱网（≲2 Mbps）」两个条件同时成立时，传输时间才升到 5–10 s 量级，此时外部从"放大器"升级为"并列主因"。

### 4.2 10 s axios 超时的临界条件（明确公式 + 实测参数）

前端：所有历史请求走 `frontend-shared/src/api/client.ts:46` 的 **`timeout: 10000`**；仅 latest-data 在 `edgeDevice.ts:385-389` 放宽到 20 s。

```
超时 ⟺ T_server(该请求在"页面并发 + 生产负载"下的服务端耗时)
        + T_ext(连接 RTT + 慢启动 + Bytes/上行带宽)     > 10 s
```

用实测代入：

| 请求 | T_server（6 并发，LAN，Lead §2.3） | 距离 10 s 余量 | 正常宽带+gzip 的 T_ext（S1/S2） | 外部是否改变结果 |
|---|---:|---:|---:|---|
| cells 16×24h | **9.79 s** | **+0.21 s** | 0.10–0.31 s | **会（LAN 差 0.21 s，外部 +0.1–0.3 s 即越线）** |
| metrics 9×24h | 12.40 s | −2.40 s | 0.05–0.15 s | 不会（LAN 已超时） |
| protection 30d | 16.70 s | −6.70 s | 0.03–0.08 s | 不会（LAN 已超时） |
| temp 3×24h | 7.72 s | +2.28 s | 0.03–0.12 s | 正常宽带不会；弱网/无 gzip 才可能 |
| soc+current | 12.35 s | −2.35 s | 0.02–0.08 s | 不会（LAN 已超时） |
| latest-data | 11.04 s | −1.04 s | 0.01–0.05 s | 不会（LAN 已超时；它自己 20 s 上限） |

**临界条件成立的关键前提**：① 单个请求的服务端耗时已经贴近 10 s（当前 cells=9.79 s 恰好落在外部能补刀的位置）；② 外部 T_ext 大于剩余余量；③ 一旦多 1 个请求超时 → `useHistoryData.ts:99-120` 触发**逐测点降级**（cells 超时 = 16 个新请求），服务端负载又被抬高 → 更多请求越线（正反馈）。实测 A-lan 12 失败、B2 28 失败即为该机制。

反之，若修复了服务端（P0-1/P0-2 后 <1–2 s），外部 T_ext（≤0.3 s）相对 10 s 只有 3%，**同样错也基本不会因外部而超时**；只有在"无 gzip + ≤2 Mbps"的病态链路下才需要担心。

### 4.3 多请求并发时，带宽竞争会不会恶化？

- **gzip 生效 + ≥10 Mbps：不会。** 首屏 8 个主响应 gzip 合计 ≈317 KB；整页 44 个 API 实测编码 267,739 B。5 个响应同时下载也只占 0.2–1.1 s 的链路时间（10/2 Mbps），而页面墙钟 16–21 s ⇒ 带宽利用率 <10%，瓶颈是服务端 CPU/PG，不是链路。
- **无 gzip：会变得可观。** 解码后整页 2,390,210 B（前端取证 §3.1）+ 5 个主响应 identity 合计 3.07 MB；2 Mbps 下光"把并发响应搬下来"就需 9.6–12.3 s，且 5 条 TCP 在一条上行里公平分享 ⇒ 尾部响应被拉长到秒级。
- **gzip 是否生效由服务端决定，不由链路决定**：18090 无 TLS、无反代、无中间盒，浏览器 `Accept-Encoding: gzip` 端到端直达 Go 的 `gin-contrib/gzip`（§5.1）。**除非用户侧有企业代理/运营商透明代理剥掉 Accept-Encoding 或重压缩失败，否则外部 = LAN 同样的 gzip 体积。** 这一点没有真实外部实测，属推断（§8）。

---

## 5. gzip / HTTP/1.1 / keep-alive / WS 在外部链路上的行为

### 5.1 gzip：服务端已开，实测协商成功【实测】

源码：`backend/cmd/server/main.go:468-479`
```go
// Gzip compression for API responses (20MB JSON → ~3MB).
r.Use(gzip.Gzip(gzip.DefaultCompression, gzip.WithExcludedPaths([]string{
    "/metrics", "/api/v1/metrics/prometheus",
})))
```

实测（无任何反代，直连 18090；curl `-H 'Accept-Encoding: gzip' --compressed`）：
```
RESULT|cells-gz|code=200|ver=1.1|ttfb=5.718|total=5.737|dl=164958
HDR|cells-gz|Content-Encoding: gzip
HDR|cells-gz|Vary: Accept-Encoding
HDR|cells-gz|Transfer-Encoding: chunked
```
- 未压缩请求（无 Accept-Encoding）：无 `Content-Encoding`，同样 `chunked`。
- 协商字段完整（`Vary` 已设，缓存代理可区分），`Content-Length` 缺失是 chunked 的正常结果，**不影响浏览器**。
- 结论：**外部链路（无论是 TS 隧道还是公网 IPv6）都是纯 TCP 透传，gzip 体积与 LAN 完全一致**——除非中间盒改 Accept-Encoding（未观测到、也无中间盒在 18090 路径上）。

### 5.2 协议版本：**仅 HTTP/1.1**（无 h2/h3、无 TLS）【实测】

```
$ curl -s --http2-prior-knowledge http://192.168.20.6:18090/api/v1/edge-devices/14
curl: (16) Remote peer returned unexpected data while we expected SETTINGS frame. Perhaps, peer does not support HTTP/2 properly.
$ curl -o /dev/null -w 'ver=%{http_version}\n' ...        # → ver=1.1
```
⇒ 页面是 `http://…:18090`（明文），浏览器只能 h1.1，最多 6 个并行连接/origin。首屏 8 个请求中 5 个重查询本来就并发、且各占 1 条连接，**不会因协议版本额外排队**；但 h2 的多路复用/优先级也帮不上忙（本来就不存在）。

### 5.3 keep-alive：可用且被复用【实测】

```
$ curl 同一命令行依次请求 3 个端点（HTTP/1.1 默认 keep-alive）
x1 nconn=1 connect=0.000886 total=0.009101   # detail
x2 nconn=0 connect=0.000000 total=0.294907   # latest-data（复用连接）
x3 nconn=0 connect=0.000000 total=0.003672   # command-intervals（复用）
```
外部链路不影响 keep-alive：TS 是透明 TCP；IPv6 直连无 NAT；CPE 状态防火墙对 established 放行。

### 5.4 WebSocket：直连路径 101 正常；链路上没有 nginx 可掐断【实测 + 代码】

```
$ curl -i -N -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' \
    -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H "Authorization: Bearer <JWT>" \
    -H 'Origin: http://192.168.20.6:18090' http://192.168.20.6:18090/api/v1/ws | head -5
HTTP/1.1 101 Switching Protocols
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=
（随后持续收到 node_status / channel_data / data_update 帧，1 Hz）
```

- **中间层**：18090 路径上没有 nginx/反代（§2.1/§2.2），不存在 `proxy_read_timeout`、`proxy_send_timeout`。NPM 只代理 80/443 的其它站点。CPE 是 MikroTik 状态防火墙（IPv6 无 NAT）；WS 有 **1 Hz 业务数据帧**，不会触发空闲老化；若用户走 Tailscale，WireGuard 自身有 keepalive。
- **后端 WS 无超时/无 ping**：`backend/internal/websocket/websocket.go:244-303`，`HandleWebSocket` 升级后起 `readPump/writePump`，`readPump` 直接 `ReadMessage()` 循环，**没有 SetReadDeadline、没有服务端 Ping**（grep `PingPeriod|PongWait|SetReadDeadline` 无命中）。⇒ 掐断只可能来自网络（移动网络切换、CGNAT/AP 状态丢失、DERP relay 切换），不会来自服务端。
- **前端重连代价很小**：`frontend-shared/src/stores/websocket.ts`
  - `:507-517` 应用层心跳 `{"type":"ping"}`，45 s 间隔（收到任何消息即重置）；
  - `:420-435` 断线先 `GET /api/v1/account`（`AbortSignal.timeout(4000)`）判定凭证是否有效（只发 1 个轻量请求）；
  - `:443-461` 指数退避重连 **5→10→20→40→60 s**。
- **重连不会重取历史数据**：BMS 页（`BmsDetailPage.vue`）没有注册 `wsStore.onConnected`；全局只有 `MainLayout.vue:586` 在重连后重新拉「通知」；诊断卡的 `watch([deviceId, online])` 由详情接口/设备切换驱动，不由 WS 驱动。⇒ **WS 断连重连与"反复 1.5 MB 请求"无关**；"反复请求"来自 `useHistoryData` 的超时降级（前端取证 §4）。

---

## 6. 「外部访问放大系数」

### 6.1 首屏墙钟（含并发争用）

| 口径 | 墙钟 | 来源 |
|---|---:|---|
| LAN，6 并发（无降级风暴） | **16.71 s** | Lead 并发实验（生产 loadavg 起始≈3，并发本身拉高） |
| LAN，真实浏览器（含降级风暴） | **约 20.7 s**（最后一个请求结束；44 API/12 失败） | 前端取证 A-lan |
| LAN，5 个历史请求串行合计 | 20.6–21.1 s | Lead 基线/复测 |
| **外部估计（正常宽带 + gzip）** | **16.8–17.6 s** | 16.71 + 0.03–0.3 s/请求（并发后取最大增量 + 可能多 1 个超时→降级 +0–4 s） |
| 外部估计（弱网 2–10 Mbps + gzip） | 17.5–19 s | S2/S3 增量 |
| 外部估计（无 gzip，10 Mbps） | 18.5–20 s+ | S4 增量 + 更多超时 |
| 外部估计（无 gzip + ≤2 Mbps） | **25–30 s+** | S5 增量 + 降级风暴 |
| 外部估计（CDP 模拟 500 ms RTT/1.6 Mbps） | 录制 80 s 内 4 卡"暂无"、28 失败（非墙钟口径） | 前端取证 B2（模拟） |

### 6.2 放大系数汇总

| 维度 | 正常宽带 + gzip | 弱网（≲2 Mbps）+ gzip | 无 gzip（正常宽带） | 无 gzip + 弱网 |
|---|---:|---:|---:|---:|
| 首屏墙钟放大（外部/LAN，以 16.7 s 为基） | **1.00–1.05×** | 1.05–1.14× | 1.11–1.20× | **1.5–1.8×+** |
| 单请求 cells（5.64 s→） | 1.02–1.03× | 1.12–1.19× | 1.24–1.29× | 2.1–2.2× |
| 单请求 protection（9.87 s→） | 1.003–1.006× | 1.02–1.04×（越线） | ~1.02×（越线） | 1.05–1.07× |
| 失败数放大（44 API） | 12 → 12–28（多 1 个 batch 超时即 +16 降级请求、再失败若干） | 12→28（实测模拟口径） | ≥12，可能 28 | 28–40 |
| 首屏传输时间（总字节/上行） | 267,739 B：30 Mbps=0.07 s，10 Mbps=0.21 s | 1.07 s | 2,390,210 B：10 Mbps=1.9 s | 9.6 s |

**一句话**：**外部对"墙钟"只是 1.0–1.2 倍的放大器；但对"超时/失败/降级风暴"是阈值型放大器**——把本来就在 9.8 s 的 cells 推过 10 s、把弱网下本来 8 s 的 temp 推过 10 s，每推过一个就多 16～22 个降级重查询，再反过来拖慢所有请求。**根因是服务端 20.6 s 串行成本 + 前端 5 并发编排 + 10 s 超时降级机制；外部只是最后 3% 的推手。**（与根因分析 §0/R6 一致。）

---

## 7. 可复现命令（本文所有测量，只读）

```bash
# 0) 登录生产只读（凭据见 .secrets/prod.env；本文使用 sshpass 内联口令，勿入库）
S='sshpass -p *** ssh -F /dev/null -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null root@192.168.20.6'

# 1) 外部入口排查
$S 'for f in /srv/nginx-proxy-manager/data/nginx/proxy_host/*.conf; do echo "##### $f"; cat "$f"; done'
$S 'grep -rn "18090" /srv/nginx-proxy-manager/data/ ; echo rc=$?'
$S 'docker ps --format "{{.Names}}|{{.Image}}|{{.Ports}}" ; ss -lntp | grep -E ":18090|:2053|:10808"'
$S 'iptables -S INPUT ; ip6tables -S INPUT ; nft list ruleset | head -20'
$S 'cat /srv/ddns-go/data/.ddns_go_config.yaml'
$S 'python3 -c "import sqlite3;con=sqlite3.connect("file:/srv/3x-ui/db/x-ui.db?mode=ro",uri=True);print(list(con.execute("select port,protocol,settings from inbounds")))"'
$S 'python3 -c "import sqlite3;con=sqlite3.connect("file:/srv/nginx-proxy-manager/data/database.sqlite?mode=ro",uri=True);print(list(con.execute("select id,domain_names,forward_host,forward_port from proxy_host")))"'
$S 'tailscale status ; tailscale status --json | head -c 2000'

# 2) 本机侧（DNS / IPv6 / Tailscale / 带宽）
dig +short A qsun.fun; dig +short AAAA qsun.fun; dig qsun.fun AAAA +noall +answer
tailscale netcheck; tailscale ping -c 3 100.64.0.7
curl -6 -sS -o /dev/null --max-time 40 'https://speed.cloudflare.com/__down?bytes=10000000' -w '%{time_total} %{speed_download}\n'
head -c 8000000 /dev/urandom > up.bin
curl -6 -sS -o /dev/null -X POST --data-binary @up.bin 'https://speed.cloudflare.com/__up' -w '%{time_total}\n'
curl -6 -sS -o /dev/null 'https://mirrors.tuna.tsinghua.edu.cn/ubuntu-releases/22.04/ubuntu-22.04.5-live-server-amd64.iso' -r 0-9999999 -w '%{time_total} %{speed_download}\n'

# 3) 生产 API（JWT 由 .tmp-verify/prod_api.sh 铸造；只读 GET）
.tmp-verify/prod_api.sh '/api/v1/unified-data/historical-batch?device_pk=14&categories=cell_voltage_1,...,cell_voltage_16&start_time=<24h-ago>&end_time=<now>&max_points=500' \
  -o /dev/null -w 'code=%{http_code} ver=%{http_version} connect=%{time_connect} ttfb=%{time_starttransfer} total=%{time_total} dl=%{size_download}\n'
.tmp-verify/prod_api.sh '/api/v1/unified-data/historical-batch?...' -H 'Accept-Encoding: gzip' --compressed -D - -o /dev/null
# 传输分量验证（用客户端限速模拟带宽约束，不碰生产）：
.tmp-verify/prod_api.sh '...cells...' --limit-rate 200000 -o /dev/null -w 'ttfb=%{time_starttransfer} total=%{time_total} dl=%{size_download}\n'
# WS 握手
.tmp-verify/prod_api.sh '' -i -N --max-time 6 -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  -H 'Origin: http://192.168.20.6:18090' '/api/v1/ws' | head -5
```

原始输出留档：`.tmp-network/`（jwt.txt、env.vars、conc/、load.log、hdr.last、prot_ts.json；临时 scratch，未入库）。

---

## 8. 未证实项与限制（必须与结论一起引用）

1. **未复现真实外部路径（核心限制）**：本机与生产同 LAN、同 `/64`；本地 IPv6 测量 RTT 0.79 ms、traceroute 1 跳（on-link），**不代表公网**；本地 Tailscale 是 LAN direct（1 ms），**不代表远程 overlay**（远程可能走 hk/fkr DERP relay）。没有可执行 1.5 MB 级测量的真实外部 vantage。
2. **外部可达性未证实**：IPv6:18090 的入站放行由 MikroTik 配置决定，未取得配置（未尝试登录）；check-host 的 HTTP 探测因不支持 AAAA-only 域名而失败（ENXIO），**不能证明可达也不能证明不可达**；IPv4:18090 在 3 个境外节点全部超时，但双 WAN/动态 IP + 样本少 ⇒ 只能作为"未发现 IPv4 映射"的弱证据。**用户实际走 A（Tailscale）还是 B（公网 IPv6），建议由用户外网实测确认一次（见 §8.1）。**
3. **带宽假设**：家宽上行用 Cloudflare 跨境的 26 Mbps 作下限、TUNA 国内下行 111 Mbps 作参考；**国内上行没有测到**（找不到国内可写上传端点），S1 的 30 Mbps 是假设。外部用户的 RTT 也只能用 AliDNS 11 ms / Cloudflare 176 ms / DERP 41 ms 三个锚点外推。
4. **本文的耗时测量受本轮取证负载污染**（loadavg 8–18，3 vCPU）：只采用其体积/压缩比/协议事实；耗时基线一律引用 Lead（loadavg≈3）。我自己的 8 并发实验（21:18，9.98→12.56）墙钟 16.24 s、单请求 3–16 s，仅作"并发上界"，不作基线。
5. **gzip 未被中间盒剥离是推断**：依据是 18090 路径无 nginx/反代、无 TLS 中间层，端到端 TCP；但用户侧代理/运营商透明代理是否改 Accept-Encoding 无外部实测。
6. **无 gzip 场景未在真实浏览器上验证**：S4/S5 是模型计算；浏览器 A-lan 实测已证明 gzip 生效（encoding 267 KB vs decoded 2.39 MB）。
7. **B2-throttle 是模拟**（CDP 500 ms RTT/200 KB/s），不是真实外部；它给的是"外部慢网近似上界"，不能替代真实链路。
8. **首访静态资源成本未测**：前端取证记录本次所有 JS/CSS 命中浏览器缓存，首个访问者还要下约 42 个 script chunk（未计入字节）；纯外网首访会多一段传输时间（带宽相关，本文未量化）。
9. **IPv6 单栈风险（外部特有）**：`qsun.fun` 只有 AAAA ⇒ 在"有 IPv6 地址但路由不通"的网络里，浏览器不会回退 IPv4，表现是**连接阶段就挂起**（不是慢），需用户确认自己网络 IPv6 可用；Tailscale 路径不受此影响。非标端口 18090 也可能被部分企业网/公共 Wi-Fi/安全网关直接拦截（失败而非变慢）。

### 8.1 建议的用户外网验证（1 分钟，只读）

从家外（手机 4G/5G 或公司网）分别试：
```
A) http://100.64.0.7:18090/          # 需已登录 Tailscale
B) http://qsun.fun:18090/            # 需 IPv6
C) 同 A/B 打开 /edge-device/14，看图表是否"加载 → 暂无数据"
浏览器 DevTools → Network：记录 5 个 historical-batch 的 TTFB、是否 10 s 超时、编码字节
```
把"实际能打开的是 A 还是 B"和该次 Network 的 TTFB 贴回，即可把本文 §3.3 的模型估计钉成真实测量（补一次即可，不要反复压测）。

---

## 9. 与另两份取证的交叉引用

| 本文结论 | 交叉证据 |
|---|---|
| 服务端 TTFB 5.6–9.9 s 是主因 | 后端取证 §2（EXPLAIN）、Lead 基线；前端取证 A-lan TTFB（8.6/10.0 s） |
| 10 s 超时 → 22 请求降级风暴 | 前端取证 §4（机制+实测：4/5 超时→22 降级→8 再超时） |
| 整页编码字节 267,739 B / 解码 2,390,210 B | 前端取证 §3.1/§8 |
| 外部慢网模拟：5/5 超时、28 失败、全部图表无数据 | 前端取证 §4.3（B2-throttle） |
| gzip 9.5×、仅 h1.1、无 Content-Length | 本文 §3.1/§3.2/§5.1/§5.2；后端取证 §6 亦有 gzip 中间件与体积交叉 |
| 外部是放大器不是根因 | 根因分析 §0/§3 R6；本文给出放大系数与成立条件 |
