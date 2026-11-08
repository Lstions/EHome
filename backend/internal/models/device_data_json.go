package models

// device_data_json.go — device_data.data_json 的压缩编解码（GZ1 格式）。
//
// ==================== 为什么需要它 ====================
//
// device_data 存的是**原始 JSON**（解析前/后的完整报文），不受 L1（写收窄）与
// L2（帧级编码）任何影响 —— 它是遥测存储优化里唯一的漏网之鱼，占 L1/L2 之后
// 剩余每日增长的 74%（生产实测 150 MB/天）。
//
// 实测：整体 gzip-6 压缩率 9.2x（168,967 B → 18,310 B），其中 86% 的字节是
// sensors 字段（与 unified_data / telemetry_frame 100% 重复，但那属于另一个
// 任务的处置范围 —— 本任务只做**无损压缩**，不改任何内容）。
//
// ==================== 为什么是 `GZ1:` + base64(gzip)，不是 bytea ====================
//
// 改列类型（TEXT → BYTEA）要重写 4.4 GB / 560 万行，且分区母表 ALTER TYPE
// 会级联到所有子分区，锁持有时间与回滚成本都不成比例。而 TEXT 列完全装得下
// base64 —— 代价是体积放大 4/3，9.2x 变 6.9x，仍然值得。
//
// 前缀 `GZ1:` 的设计要点：
//  1. **判别廉价**：strings.HasPrefix，不需要解析、不需要额外列；
//  2. **版本化**：将来换算法用 GZ2: / ZSTD:，老版本的读路径仍能解老格式；
//  3. **存量明文永远兼容**：没有前缀 = legacy 明文 JSON，原样返回。
//     这是「混存」不炸的根本原因 —— 存量 4.4 GB 不需要先迁移才能读。
//
// ==================== 三条不变式（改动前必读）====================
//
//	I1. 编码/解码互逆：Decode(Encode(raw)) == raw，逐字节相等（无损）。
//	I2. 编码幂等：Encode(Encode(raw)) 不会二次压缩 —— 否则历史压缩任务重跑
//	    会变成 double-gzip，解一层出来的还是压缩数据（本任务的头号翻车点）。
//	I3. 失败不丢数据：编码失败时**返回明文**（空间换不回来可以再压，数据丢了
//	    就没了）；解码失败时**返回原文**（前端拿到压缩串至少能看见"这是压缩的"，
//	    返回空串则表现为"设备没数据"，更糟）。

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

// DeviceDataJSONPrefix 是压缩载荷的版本前缀。
//
// 选 `GZ1:` 而不是带二进制的魔数：TEXT 列里有非可打印字符会让
// psql / 日志 / 导出工具都变难用，而 base64 后的载荷本来就是可打印 ASCII，
// 加一个可读前缀不引入任何新问题。
const DeviceDataJSONPrefix = "GZ1:"

// gzipCompressionLevel 固定为 6。
//
// 为什么不取 BestCompression（9）：实测 level 9 对这批 JSON 只比 6 好 2~3%，
// 而写入路径是热路径（每帧一次），CPU 代价不成比例。也不取 BestSpeed（1）：
// 压缩率掉到 5x 左右，省下的 CPU 换不回磁盘 —— 而磁盘才是本任务的瓶颈。
const gzipCompressionLevel = 6

// EncodeDeviceDataJSON 把明文 JSON 压缩成可存 TEXT 的载荷。
//
// 返回 `GZ1:<base64(gzip(raw))>`；**任何失败都返回原始明文**（I3），
// 因此调用方可以无条件把它当作「能存的值」使用，不需要判错分支
// （写路径上多一个 error 分支，迟早有人在那个分支里 return 掉不写库）。
//
// I2（幂等）：输入已带前缀时原样返回。历史压缩任务与写路径用的是同一个函数，
// 这个检查让「重跑一次」天然安全 —— 不依赖调用方记得先查前缀。
func EncodeDeviceDataJSON(raw []byte) string {
	if len(raw) == 0 {
		return string(raw)
	}
	// I2: 已经是压缩载荷 ⇒ 不再压。见本文件头 I2 的说明。
	if IsCompressedDeviceDataJSON(string(raw)) {
		return string(raw)
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzipCompressionLevel)
	if err != nil {
		// 只可能是 level 非法，而它是编译期常量 —— 仍然回落而不是 panic：
		// 写路径上 panic 会带走整个 consumer goroutine。
		return string(raw)
	}
	if _, err := zw.Write(raw); err != nil {
		return string(raw)
	}
	if err := zw.Close(); err != nil {
		// Close 才把 gzip trailer 写出来；这里失败同样回落明文（I3）。
		return string(raw)
	}
	// base64 用 StdEncoding（带 = 填充）而不是 RawStdEncoding：
	// 填充符让长度可校验，且 psql 里肉眼能看出截断。
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	// 防御：压缩后反而更大时不值得（极小 JSON + base64 膨胀）。
	// 实测这批数据都在 200 B 以上，这只是给将来留的后路。
	if len(DeviceDataJSONPrefix)+len(encoded) >= len(raw) {
		return string(raw)
	}
	return DeviceDataJSONPrefix + encoded
}

// DecodeDeviceDataJSON 把库里的 data_json 还原成明文。
//
// 这是「混存兼容」的唯一实现点：带前缀 ⇒ 解压；不带 ⇒ 原样返回（legacy 明文）。
//
// 失败语义（I3）：返回**原始存储值** + 非 nil error。调用方应当
//  1. 把 error 记日志（这是数据损坏的信号，必须可见）；
//  2. **仍然把返回值发给前端**（不要 500、不要空串）。
//
// 为什么 error 不叫致命：解压失败意味着这一行坏了，而其他行是好的。
// 让一行坏数据把整个历史列表打成 500，是把局部损坏放大成全局不可用。
func DecodeDeviceDataJSON(stored string) (string, error) {
	if !IsCompressedDeviceDataJSON(stored) {
		return stored, nil // legacy 明文：存量行的正常路径，不是错误
	}
	payload := stored[len(DeviceDataJSONPrefix):]
	compressed, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return stored, fmt.Errorf("device_data.compressed payload is not valid base64: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return stored, fmt.Errorf("device_data.compressed payload is not valid gzip: %w", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		return stored, fmt.Errorf("device_data.compressed payload failed to decompress: %w", err)
	}
	return string(out), nil
}

// IsCompressedDeviceDataJSON 判断一个 data_json 值是否是压缩载荷。
//
// 单独导出而不是让调用方写 HasPrefix：历史压缩任务要按它生成
// `WHERE data_json NOT LIKE 'GZ1:%'` 的幂等条件，两处必须是同一个判据。
func IsCompressedDeviceDataJSON(stored string) bool {
	return strings.HasPrefix(stored, DeviceDataJSONPrefix)
}

// DecodeDeviceDataRows 就地解码一批 DeviceData 的 DataJSON。
//
// 返回**解码失败的行数**（0 表示全部正常）。刻意不返回 error：
// 调用方要的是「尽可能多地把数据还给前端」，而不是「有一行坏就整批失败」。
// 失败行的 DataJSON 保持原文（I3），调用方只需在 count > 0 时记一条聚合日志 ——
// 逐行打日志会在一个坏分区上刷爆磁盘。
func DecodeDeviceDataRows(rows []DeviceData) int {
	failed := 0
	for i := range rows {
		decoded, err := DecodeDeviceDataJSON(rows[i].DataJSON)
		if err != nil {
			failed++
			// 保持原文（DecodeDeviceDataJSON 已经保证了这一点，这里显式赋值
			// 是为了让「解压失败也不改内容」在代码上一眼可见）。
			rows[i].DataJSON = decoded
			continue
		}
		rows[i].DataJSON = decoded
	}
	return failed
}
