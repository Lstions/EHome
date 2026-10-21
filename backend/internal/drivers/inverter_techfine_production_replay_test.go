package drivers

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ============================================================================
// 生产帧回放用例 —— 用真实设备的应答帧锁住解析结果
// ============================================================================
//
// testdata/techfine_production_frames.txt 里的每一行都是**生产设备真实应答**
// （device_id=15 / node F0F5BD02DD84），期望值是生产库落库值（除 read_temperature
// 按 2026-10-21 修正后的风扇语义重算，见文件头注释）。
//
// 这组用例的价值：它是**唯一**能在 CI 里发现"解析器悄悄变了"的手段。
// 单元夹具是人造的，容易和实现一起漂移；真实帧不会。
//
// 其中 read_temperature 段里包含 8 条**历史污染帧**（2026-10-18/19 被旧实现
// 读成 battery_*，生产污染 2501 条），期望值明确写的是温度+风扇，
// 因此"退回旧行为"会立刻变红。

type productionFrame struct {
	cmd    string
	raw    string
	expect map[string]float64
	line   int
}

func loadProductionFrames(t *testing.T) []productionFrame {
	t.Helper()
	path := filepath.Join("testdata", "techfine_production_frames.txt")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开生产帧样本失败（该文件是回归防线，不能删）: %v", err)
	}
	defer f.Close()

	var out []productionFrame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			t.Fatalf("%s:%d 格式错误（应为 cmd|raw|expect）: %q", path, lineNo, line)
		}
		expect := map[string]float64{}
		for _, kv := range strings.Split(parts[2], ";") {
			if kv == "" {
				continue
			}
			eq := strings.IndexByte(kv, '=')
			if eq <= 0 {
				t.Fatalf("%s:%d 期望值格式错误: %q", path, lineNo, kv)
			}
			v, err := strconv.ParseFloat(kv[eq+1:], 64)
			if err != nil {
				t.Fatalf("%s:%d 期望值不是数字: %q", path, lineNo, kv)
			}
			expect[kv[:eq]] = v
		}
		out = append(out, productionFrame{cmd: parts[0], raw: parts[1], expect: expect, line: lineNo})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读取生产帧样本失败: %v", err)
	}
	if len(out) < 40 {
		t.Fatalf("生产帧样本只有 %d 条（<40），回归防线被削弱", len(out))
	}
	return out
}

// TestTechfineProductionFramesReplay 回放全部生产帧并逐字段比对。
//
// 它凭什么会失败：任何一个 parser 的取值、单位映射、位序、字段数判定被改动，
// 都会在这里变成"期望 X 实际 Y"。
func TestTechfineProductionFramesReplay(t *testing.T) {
	frames := loadProductionFrames(t)
	d := &TechfineInverterDriver{}

	perCmd := map[string]int{}
	for _, fr := range frames {
		c, ok := techfineCommandByID[fr.cmd]
		if !ok {
			t.Errorf("样本引用了未登记的命令 %q（第 %d 行）", fr.cmd, fr.line)
			continue
		}
		raw := []byte(fr.raw + "\r")
		got, err := d.ParseDataWithCommand(raw, hex.EncodeToString([]byte(c.Wire)))
		if err != nil {
			t.Errorf("第 %d 行 %s 解析失败: %v\n  raw=%s", fr.line, fr.cmd, err, fr.raw)
			continue
		}
		gotMap := map[string]float64{}
		for _, s := range got {
			gotMap[s.Name] = s.Value
		}
		if len(gotMap) != len(fr.expect) {
			t.Errorf("第 %d 行 %s 字段数不符: 期望 %d 个，实际 %d 个\n  期望=%v\n  实际=%v",
				fr.line, fr.cmd, len(fr.expect), len(gotMap), fr.expect, gotMap)
			continue
		}
		bad := false
		for name, want := range fr.expect {
			gv, exists := gotMap[name]
			if !exists {
				t.Errorf("第 %d 行 %s 缺少字段 %s（期望=%.4f）", fr.line, fr.cmd, name, want)
				bad = true
				continue
			}
			if gv != want {
				t.Errorf("第 %d 行 %s 字段 %s: 期望 %.4f 实际 %.4f\n  raw=%s", fr.line, fr.cmd, name, want, gv, fr.raw)
				bad = true
			}
		}
		if !bad {
			perCmd[fr.cmd]++
		}
	}

	// 覆盖度守卫：确保每个主要命令都有真实帧参与回放。
	for _, id := range []string{"read_status", "read_grid", "read_output", "read_battery", "read_pv1", "read_temperature", "read_energy"} {
		if perCmd[id] == 0 {
			t.Errorf("命令 %s 没有任何生产帧通过回放（覆盖度不足）", id)
		}
	}
}

// TestTechfineProductionTemperatureFramesNeverYieldBattery 是 P0 的行为级防线。
//
// 历史缺陷：8 字段温度帧经旧 ParseData 的兜底分支被读成 5 个电池字段，
// 生产实测污染 battery_voltage 2501/26847 条（value<45V，正常应 48~58V）。
// 本用例对**每一条**温度帧同时检查"有上下文"和"无上下文"两条路径。
func TestTechfineProductionTemperatureFramesNeverYieldBattery(t *testing.T) {
	frames := loadProductionFrames(t)
	d := &TechfineInverterDriver{}
	cmd := techfineCommandByID["read_temperature"]
	if cmd == nil {
		t.Fatal("注册表缺少 read_temperature")
	}

	checked := 0
	for _, fr := range frames {
		if fr.cmd != "read_temperature" {
			continue
		}
		checked++
		raw := []byte(fr.raw + "\r")
		for _, got := range mustParseBothWays(t, d, raw, cmd) {
			for _, s := range got {
				if strings.HasPrefix(s.Name, "battery_") || s.Name == "bus_voltage" {
					t.Fatalf("温度帧产出了电池字段 %s=%.1f（raw=%s）—— 2026-10-19 污染 2501 条的同型缺陷",
						s.Name, s.Value, fr.raw)
				}
			}
		}
	}
	if checked < 8 {
		t.Fatalf("温度帧样本只有 %d 条，覆盖不足", checked)
	}
}

func mustParseBothWays(t *testing.T, d *TechfineInverterDriver, raw []byte, cmd *techfineCommand) [][]SensorData {
	t.Helper()
	var out [][]SensorData
	// 路径 1：有命令上下文（生产主路径）
	got, err := d.ParseDataWithCommand(raw, hex.EncodeToString([]byte(cmd.Wire)))
	if err != nil {
		t.Fatalf("有上下文的温度帧解析失败: %v", err)
	}
	out = append(out, got)
	// 路径 2：无命令上下文（诊断/兼容兜底路径）
	got2, err := d.ParseData(raw)
	if err != nil {
		// 允许"形状不足以自证"而报错——那正是 fail-closed 的正确行为。
		return out
	}
	out = append(out, got2)
	return out
}

// TestTechfineProductionFrameParseIsReferenceStable 保证回放样本自身合法：
// 每行期望值必须与其原始帧字段自洽（防止有人为了让用例变绿而改期望值）。
func TestTechfineProductionFrameParseIsReferenceStable(t *testing.T) {
	frames := loadProductionFrames(t)
	for _, fr := range frames {
		if fr.cmd != "read_temperature" {
			continue
		}
		f := strings.Fields(strings.TrimRight(strings.TrimPrefix(fr.raw, "("), "\r"))
		if len(f) != 8 {
			t.Fatalf("温度帧样本字段数应为 8，实际 %d: %q", len(f), fr.raw)
		}
		// 关键字段必须与原始帧一致（抽查 3 个位置 + 风扇状态）
		pairs := []struct {
			name string
			idx  int
		}{{"inverter_temp", 1}, {"max_temp", 4}, {"fan1_speed", 5}}
		for _, p := range pairs {
			want, err := strconv.ParseFloat(f[p.idx], 64)
			if err != nil {
				t.Fatalf("样本字段非数字: %q", f[p.idx])
			}
			if got := fr.expect[p.name]; got != want {
				t.Fatalf("样本 %q 的 %s 期望值 %.1f 与其原始帧字段 %.1f 不一致（期望值被改写？）",
					fr.raw, p.name, got, want)
			}
		}
		wantStatus := 1.0
		if f[7][0] != '1' {
			wantStatus = 0
		}
		if fr.expect["fan1_status"] != wantStatus {
			t.Fatalf("样本 %q 的 fan1_status 期望值与其状态位不一致", fr.raw)
		}
	}
}

var _ = fmt.Sprintf
