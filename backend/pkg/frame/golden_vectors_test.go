package frame

// golden_vectors_test.go —— S0：后端（Go）消费【共享】golden vector
//
// 为什么需要它：ESP32（components/msgcodec，C）与后端（pkg/frame，Go）是两套
// 独立实现，各自带着各自的黄金向量 ⇒ 两端可能【同时自洽却互相不通】。
// 设计文档 §3 的 S0 阶段要求"冻结契约"，判据是"两端各有一套对锚测试"。
//
// 本测试**直接读** protocol/vectors/wire_primitives.txt（唯一向量来源）：
//   1. 按用例字段编码 == wire
//   2. wire 解回 == 用例字段（顺序扫描，无尾随）
// 向量文件改了而 Go 实现没跟上（或反之），这里立刻红。
//
// 2026-10-06 的价值证明：本文件上线当天就发现 C 端骨架自创了
// [field_id][len][value] 格式，与 Go/C 生产实现的 tag=(field<<3)|wire 不兼容。
// 在那之前，两端各自的测试**都是绿的**。

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type vecField struct {
	kind    string // "u64" | "bytes"
	fieldNum uint8
	u64      uint64
	data     []byte
}

type vecCase struct {
	name   string
	line   int
	isSub  bool
	msgType uint8
	fields []vecField
	wire   []byte
}

// findVectors 定位共享向量文件（相对本测试文件上溯到仓库根的 protocol/vectors）。
func findVectors(t *testing.T) string {
	t.Helper()
	// backend/pkg/frame -> ../../.. => worktree 根
	cand := filepath.Join("..", "..", "..", "protocol", "vectors", "wire_primitives.txt")
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	t.Fatalf("找不到共享向量文件 %s（S0 契约缺失 ⇒ 本测试无法对锚）", cand)
	return ""
}

func parseHexField(t *testing.T, s string, line int) []byte {
	t.Helper()
	if s == "-" {
		return []byte{}
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("第 %d 行：hex 非法 %q: %v", line, s, err)
	}
	return b
}

func parseVectors(t *testing.T, path string) []vecCase {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读向量文件失败: %v", err)
	}
	var cases []vecCase
	var cur *vecCase
	for i, text := range strings.Split(string(raw), "\n") {
		line := i + 1
		s := strings.TrimSpace(text)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		parts := strings.Fields(s)
		switch parts[0] {
		case "case":
			if cur != nil {
				t.Fatalf("第 %d 行：上一个 case 未用 end 收尾", line)
			}
			cur = &vecCase{name: parts[1], line: line}
		case "sub":
			cur.isSub = true
		case "type":
			v, err := strconv.Atoi(parts[1])
			if err != nil {
				t.Fatalf("第 %d 行：type 非法", line)
			}
			cur.msgType = uint8(v)
		case "u64":
			fn, _ := strconv.Atoi(parts[1])
			v, err := strconv.ParseUint(parts[2], 10, 64)
			if err != nil {
				t.Fatalf("第 %d 行：u64 值非法 %q", line, parts[2])
			}
			cur.fields = append(cur.fields, vecField{kind: "u64", fieldNum: uint8(fn), u64: v})
		case "bytes":
			fn, _ := strconv.Atoi(parts[1])
			cur.fields = append(cur.fields, vecField{
				kind: "bytes", fieldNum: uint8(fn), data: parseHexField(t, parts[2], line)})
		case "wire":
			cur.wire = parseHexField(t, parts[1], line)
		case "end":
			if cur.wire == nil {
				t.Fatalf("第 %d 行：case %s 缺少 wire", line, cur.name)
			}
			cases = append(cases, *cur)
			cur = nil
		default:
			t.Fatalf("第 %d 行：未知关键字 %q", line, parts[0])
		}
	}
	if cur != nil {
		t.Fatalf("文件末尾的 case %s 未收尾", cur.name)
	}
	return cases
}

func TestGoldenVectorsFromSharedContract(t *testing.T) {
	path := findVectors(t)
	cases := parseVectors(t, path)
	if len(cases) == 0 {
		t.Fatal("向量文件里没有任何用例 —— 契约文件被清空了？")
	}

	failures := 0
	for _, c := range cases {
		// --- 1. 编码 ---
		var enc *Encoder
		if c.isSub {
			enc = SubEncoder()
		} else {
			enc = NewEncoder(c.msgType)
		}
		for _, f := range c.fields {
			switch f.kind {
			case "u64":
				enc.EncodeVarint(f.fieldNum, f.u64)
			case "bytes":
				enc.EncodeBytes(f.fieldNum, f.data)
			}
		}
		if !bytes.Equal(enc.Bytes(), c.wire) {
			t.Errorf("%s（第 %d 行）编码不符：go=%x 契约=%x",
				c.name, c.line, enc.Bytes(), c.wire)
			failures++
			continue
		}

		// --- 2. 解码（顺序扫描 + 无尾随）---
		var dec *Decoder
		var err error
		if c.isSub {
			dec, err = NewSubDecoder(c.wire)
		} else {
			dec, err = NewDecoder(c.wire)
		}
		if err != nil {
			t.Errorf("%s：建解码器失败 %v", c.name, err)
			failures++
			continue
		}
		if !c.isSub && dec.MsgType() != c.msgType {
			t.Errorf("%s：类型字节 %d != %d", c.name, dec.MsgType(), c.msgType)
			failures++
		}
		bad := false
		for i, want := range c.fields {
			f, err := dec.NextField()
			if err != nil {
				t.Errorf("%s：第 %d 个字段解码失败 %v", c.name, i, err)
				bad = true
				break
			}
			if f.FieldNum != want.fieldNum {
				t.Errorf("%s：第 %d 个字段号 %d != %d", c.name, i, f.FieldNum, want.fieldNum)
				bad = true
			}
			switch want.kind {
			case "u64":
				if f.WireType != WireVarint {
					t.Errorf("%s：第 %d 个字段 wire type %d != varint", c.name, i, f.WireType)
					bad = true
				}
				got, ok := f.Value.(uint64)
				if !ok || got != want.u64 {
					t.Errorf("%s：第 %d 个 u64 值 %v != %d", c.name, i, f.Value, want.u64)
					bad = true
				}
			case "bytes":
				got, ok := f.Value.([]byte)
				if !ok {
					t.Errorf("%s：第 %d 个字段不是 bytes", c.name, i)
					bad = true
				} else if !bytes.Equal(got, want.data) {
					t.Errorf("%s：第 %d 个 bytes %x != %x", c.name, i, got, want.data)
					bad = true
				}
			}
			if bad {
				break
			}
		}
		if !bad {
			// 必须恰好解析完（无尾随数据）
			if _, err := dec.NextField(); err != ErrEndOfFrame {
				t.Errorf("%s：解析未恰好结束（期望 ErrEndOfFrame，得到 %v）", c.name, err)
				failures++
			}
		} else {
			failures++
		}
	}

	t.Logf("对锚 %d 条共享向量，%d 条不一致", len(cases), failures)
}
