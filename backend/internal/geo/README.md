# internal/geo — 位置来源（IP / 城市）

日出日落规则的纬度、经度、时区**只有两个来源**（2026-11-14 用户裁决）：

1. **外网 IP 定位**（`ip.go`）：服务端按部署机出口 IP 解析，三提供方顺序降级；
2. **手动选择设备城市**（`cities.json` + `geo.go` 搜索）。

时区随位置一起返回，界面没有时区输入框。

## cities.json

- 来源：[GeoNames cities15000](https://download.geonames.org/export/dump/cities15000.zip)，
  许可证 [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)。
- 筛选：
  - 中国（CN/HK/MO/TW）：人口 ≥ 3 万，或地级行政区驻地（PPLA/PPLA2/PPLC）；
  - 其它国家：人口 ≥ 50 万，或首都（PPLC）；
  - 排除 PPLX/PPLQ/PPLH/PPLW/PPLCH（城区/废弃/历史地名）。
- 名称：优先取该城市的中文别名（Han 字符，优先「XX市」去尾），否则用 GeoNames 主名。
- 时区：直接取 GeoNames 的 IANA 字段；启动加载时再用 `time.LoadLocation` 复核，
  非法条目丢弃（宁可少一个城市，也不让坏数据在运行期静默失败）。
- 结果：**2,678 条**（中国 1,717 条），按人口降序。

## 重新生成

```bash
# 下载 cities15000.zip 与 countryInfo.txt 后：
python3 scripts/gen_cities.py   # 脚本未入库（一次性数据加工）；规则见上
```

## 测试

```bash
go test ./internal/geo/
```

覆盖：目录完整性（含北京/上海/深圳/乌鲁木齐/香港/台北）、中文/拼音搜索、
IP 三提供方响应形状、降级与缓存、非法时区/越界坐标拒绝。
