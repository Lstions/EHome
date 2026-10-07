# 把证书材料铺进设备 NVS（`nvs_certs_gen.py`）

> task-26。本文件回答三件事：**为什么需要**、**怎么用**、**现在还不安全在哪**。

---

## 1. 为什么需要这个工具

固件**会读**证书 —— `main/device_link_wiring.c:349-354`：

```c
nvs_open(CONFIG_EHOME_DEVICE_LINK_NVS_NS, NVS_READONLY, &h)   // 默认 "eh_tls"
nvs_read_blob_alloc(h, "ca",   &s_ca,   &s_ca_len);
nvs_read_blob_alloc(h, "cert", &s_cert, &s_cert_len);
nvs_read_blob_alloc(h, "key",  &s_key,  &s_key_len);
```

读不到时**不假装成功**：`tls_guard` 判 `hard_fatal` ⇒ `SESSION_FATAL`（**不重试**）。
这是**有意**的 —— 不把"证书没铺开"伪装成"网络抖动"，否则现场会看到设备反复重连而查不出原因。

但仓库里**没有任何代码会写它们**：

```
$ grep -rn nvs_set_blob main/ components/ | grep -iv crash
(空)
```

⇒ 无论固件做到什么程度，真机上这条 3.0 链路**永远**停在 `SESSION_FATAL`。
本工具补的就是这一块：产出一份**可直接烧写的 NVS 镜像**。

---

## 2. 用法

### 2.1 生成 + 读回校验（一条命令）

```bash
export IDF_TOOLS_PATH=/home/sun/.espressif
export IDF_PYTHON_ENV_PATH=/home/sun/.espressif/python_env/idf6.1_py3.14_env
export IDF_PATH=/home/sun/env/esp-idf

python3 esp32-collector/tools/nvs_certs_gen.py generate \
    --ca   /path/to/ca.pem \
    --cert /path/to/device.crt \
    --key  /path/to/device.key \
    --out  /tmp/eh_tls_nvs.bin
```

默认参数与固件/分区表**对齐**，一般不用改：

| 参数 | 默认 | 依据 |
|---|---|---|
| `--namespace` | `eh_tls` | `CONFIG_EHOME_DEVICE_LINK_NVS_NS`（`main/Kconfig.projbuild:210`） |
| `--size` | `0x4000` | `partitions*.csv` 里 nvs 分区大小 |
| `--offset` | `0x9000` | 同上（仅用于打印烧写命令） |
| `--cert-bytes` | `4096` | `CONFIG_EHOME_DEVICE_LINK_CERT_BYTES`（**逐项**上限） |

退出码：`0` = 生成且**读回校验通过**；非 `0` = 不要烧这份镜像。

### 2.2 自检（不需要你准备任何材料）

```bash
python3 esp32-collector/tools/nvs_certs_gen.py selftest
```

它会现场用 `openssl` 生成一次性 CA/证书/私钥（落在 `/tmp`，**不进工作树**），然后跑：
正向（1 组真实材料 + 5 组不同尺寸）、**两条负向对照**、以及容量边界探测。

### 2.3 烧写（本工具**不替你烧**）

```bash
# 先看芯片型号；<PORT> 换成你的串口
esptool.py --chip esp32s3 -p /dev/ttyUSB0 write_flash 0x9000 /tmp/eh_tls_nvs.bin
```

⚠ `0x9000` 这个偏移来自 `partitions*.csv` 的 `nvs` 行。**换 profile 前先核对**：

```bash
grep -E '^nvs,' esp32-collector/partitions*.csv
```

⚠ **不要刷整个 flash**（`write_flash 0x0 ...`）——那会连 otadata 一起覆盖。

### 2.4 怎么确认设备真的读到了

设备起来后看串口。读到材料时会打印（`device_link_wiring.c:715`）；
读不到时是 `SESSION_FATAL`，并会打出**具体是哪个 key/为什么**：

```
证书 %s 判定为 %s（%u B，上限 %d）—— 不放宽上限
```

---

## 3. ⚠⚠ 安全现状：**当前的默认做法不安全**

### 3.1 结论先说

**本项目当前没有启用 NVS 加密。** 所以本工具默认产出的镜像里：
**设备私钥是明文**，任何能读到 flash 的人都能取出它 —— 包括物理接触设备、
或一条 `esptool.py read_flash`。**"写进 NVS"不等于"安全"。**

### 3.2 实测依据

```
$ grep -n nvs_keys esp32-collector/partitions*.csv
(空 —— 所有分区表都只有 nvs / otadata / phy_init / ota_0 / ota_1)

$ grep -rn NVS_ENCRYPTION esp32-collector/sdkconfig.defaults*
(空)
```

### 3.3 NVS 加密**能**做，但需要设备侧三件事同时具备

IDF 的工具链**已经支持**（本机 IDF 6.1 实测）：

```bash
# 生成 nvs_keys 密钥文件
python3 .../nvs_partition_gen.py generate-key --keyfile /tmp/nvs_keys.bin
# 用该密钥产出**加密**镜像
python3 nvs_partition_gen.py encrypt <csv> <out.bin> 0x4000 --keyfile /tmp/nvs_keys.bin
# 本工具已封装：nvs_certs_gen.py generate --encrypted --keyfile /tmp/nvs_keys.bin ...
```

但它**要求设备侧同时具备**：

1. 分区表里加一个 `nvs_keys` 分区（`data, nvs_keys`）→ **当前没有**；
2. sdkconfig 打开 `CONFIG_NVS_ENCRYPTION` → **当前没有**；
3. 把 `/tmp/nvs_keys.bin` 也烧进 `nvs_keys` 分区 → **当前没有这条流程**。

**缺任何一样，设备就读不了加密镜像**（表现同样是 `SESSION_FATAL`，而且更难查 ——
因为镜像看起来是好的）。所以本工具**默认不加密**，产出一份能被读回校验的明文镜像；
`--encrypted` 是给将来备好的开关，**不是**现在就该用的默认值。

⚠ 另外：`nvs_keys.bin` **本身就是一把密钥**。它同样绝不能进 git，
且应当**离线保管**（丢了 = 设备再也读不了自己 NVS 里的配置）。

### 3.4 在补齐 3.3 之前，能做的缓解措施

- **flash encryption** 若开启，会连带保护 NVS（但本项目当前也未启用，且它是**不可逆**的一次性 efuse 操作，需要独立评估）；
- 至少保证：**生成的镜像与密钥文件永不进 git**（`.gitignore` 已覆盖 `*.bin` / `*.pem` /
  `*.key` / `*.crt` / `*.der` / `*.p12` / `*.pfx`，见该文件 "证书/私钥材料" 一节）；
- 私钥按"**烧完即弃**"处理：测试/现场用的私钥单独签发，与生产签发私钥分离；
- 若设备可被物理接触，**必须**先补加密，否则等于把私钥放在明处。

> 结论一句话：**这个工具解决的是"设备拿得到材料"，不是"材料安全"。**
> 后者是本工具**未完成**的部分，已如实登记在此。

---

## 4. ⚠ 容量约束（实测，容易踩）

`CONFIG_EHOME_DEVICE_LINK_CERT_BYTES`（默认 4096）是**逐项**上限，
**不是三项之和**的上限。NVS 分区只有 `0x4000`（16384 B）。

实测（本机 IDF 6.1，三个等大 blob）：

| 材料 | 结果 |
|---|---|
| 3 × 3900 B（合计 11700 B） | 装得下 |
| 3 × 4000 B（合计 12000 B） | **装不下**（`InsufficientSizeError`） |
| 3 × 4096 B（合计 12288 B） | **装不下** |
| 4096 + 100 + 100 B | 装得下（单项拉满可以，只要合计够小） |

⇒ 三条材料**合计**应控制在约 11.7 KB 以内。超了本工具会给出明确诊断并**非零退出**，
而不是把生成器的 `InsufficientSizeError` 堆栈直接丢给你。

真的放不下时，两条路：
- 换**更短**的证书链（去掉中间 CA、用 ECC 而不是 RSA —— ECC 证书只有 RSA 的 ~1/3）；
- 放大 `nvs` 分区（改 `partitions*.csv`，并**同步** `--size`）。

---

## 5. 校验器设计说明（为什么它值得信）

### 5.1 三层校验

1. **长度**：header 行自称的 `Size` 必须等于原文件长度，且等于实际解析出的字节数；
2. **内容**：逐字节比对，不一致时报出**首个差异的字节位置**（比"不匹配"有用得多）；
3. **完整性下界**：解析出的 key 集合必须**恰好**等于期望的三个（多一个也算失败）。

### 5.2 ⚠ 一次真实的"校验器自己坏了"（值得记）

第一版解析器把 `nvs_tool.py -d blobs` 输出里"所有 2 位十六进制的词"当成数据字节。
但那一行**末尾还有一列 ASCII 渲染**（`nvs_parser.py:233-243` 的 `dump_raw`），
其中可能出现 `be` / `de` / `ab` 这类**两字符且都是十六进制**的词
⇒ 被当成数据插进 blob 中间 ⇒ 内容整体错位。

**症状是"对合法镜像报红"（假红），不是假绿** —— 但更值得记的是**它怎么被发现的**：

> 我最初的 `selftest` **通过了**。因为它只用**一组** openssl 材料，恰好没触发那个词。
> 换成另一组真实证书后立刻报红 —— 那时我才知道校验器有问题。

⇒ **教训：负向对照只能证明"该红时会红"，证明不了"该绿时真的绿"。**
正向用例必须覆盖**多组不同尺寸**（现已固定为 5 组，含 NVS 单/多条目边界 1984/1985）。

修法：按官方打印函数的结构**按 2 个以上空格切列**，只接受"整列都是 2 位十六进制"的列
（ASCII 列天然不是），并补上 §5.1 的长度硬校验。

---

## 6. 未验证 / 边界

- ❌ **没有在真机上验证过**（本卡明令禁刷设备）。工具只产出镜像 + 文档；
  "设备读到它并成功建 TLS"这一步**未被本卡证明**。
- ❌ **NVS 加密路径未端到端验证**（`--encrypted` 能生成镜像，但没有 `nvs_keys` 分区可烧，
  也没有设备验证过能读）。
- ⚠ 校验用的是 IDF 官方解析器读**镜像文件**，不是从设备 flash 读回后校验 ——
  烧写链路本身（`esptool` 写入是否正确）不在本工具覆盖范围内。
- ⚠ 本工具**不检查**证书本身的有效性（是否过期、CN/SAN 是否匹配、密钥与证书是否配对）——
  它只保证"材料被原样放进镜像"。配错材料照样会 PASS 生成，然后在设备上握手失败。
