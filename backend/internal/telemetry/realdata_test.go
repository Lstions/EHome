package telemetry

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// realdata 相对路径（生产实测导出的 unified_data CSV）。
//
// 文件由 .tmp-telemetry/pull_realdata.sh 从生产只读拉取，18 列 CSV 里的 8 列：
//
//	id,device_id,sensor_name,value,unit,timestamp,created_at,edge_device_id
//
// 为什么不把夹具提交进仓库：1.43e7 行 / 114 MB gz，任何 git 操作都会变成
// 灾难；且它是**生产数据**，入仓等于把设备遥测泄露到代码历史里。
// 测试在夹具缺席时跳过（见 locateRealdata），避免 CI 上假红。
const realdataRelPath = ".tmp-telemetry/realdata/unified_data_202611.csv.gz"

// 真实数据的两个目标设备（方案 §1.1：device14 = BMS 29 字段固定帧；
// device15 = 逆变器 7 种帧类型）。
const (
	realDeviceBMS      uint = 14
	realDeviceInverter uint = 15
)

// realdataWindow 是验收窗口：生产 2026-11-04 全天（>= 24 h，方案 §1.1/§4.5
// 的全部数字都是这一天测出来的，用同一天才能逐项对账）。
var (
	realdataFrom = time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	realdataTo   = time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
)

// realdataTSLayouts 是 CSV 里 timestamp 的可能格式。
//
// 生产导出为 "2026-11-01 00:00:00.288582+00"（Go 的 Time.String 形状）。
// 允许两种变体（有/无小数、+00 与 Z），避免换一次导出工具就整体跳过。
var realdataTSLayouts = []string{
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05-07",
}

// locateRealdata 找到真实数据夹具；找不到返回 ""（调用方 skip）。
//
// 搜索顺序：
//  1. 环境变量 EHOME_TELEMETRY_REALDATA（显式指定，CI/别的机器用）；
//  2. 从当前目录逐级向上找 .tmp-telemetry/realdata/...（容器里 cwd 是
//     /ws/.agents/worktrees/telemetry-storage/backend，向上 4 级到 /ws）；
//  3. 绝对路径 /ws/.tmp-telemetry/...（docker 里 workspace 挂在 /ws）。
func locateRealdata(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("EHOME_TELEMETRY_REALDATA"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Fatalf("EHOME_TELEMETRY_REALDATA points to missing file %q", p)
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, realdataRelPath)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if _, err := os.Stat("/ws/" + realdataRelPath); err == nil {
		return "/ws/" + realdataRelPath
	}
	return ""
}

// realFrame 是一帧真实数据：设备 + 时间戳 + 解析出的全部读数。
type realFrame struct {
	DeviceID  uint
	TS        time.Time
	Readings  []Reading
	CommandID int64 // 真实数据不带指令上下文，统一 0
}

// loadRealFrames 从 CSV 载入某设备在某窗口内的帧（按时间升序）。
//
// 分帧口径与方案 §1.1「真实帧数（微秒精度去重）」完全一致：
// **同一设备同一 timestamp 的全部行 = 一帧**。这不是随便挑的判据 ——
// 写路径每帧用一个 time.Now() 写 N 行（consumers_heavy.go:354-364），
// 所以同一帧的 N 行时间戳逐位相同。
func loadRealFrames(t *testing.T, path string, deviceID uint, from, to time.Time, maxFrames int) []realFrame {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open realdata: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer zr.Close()

	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)

	type acc struct {
		ts   time.Time
		read []Reading
	}
	byTS := make(map[int64]*acc, 1<<17)
	order := make([]int64, 0, 1<<17)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if line == "" {
			continue
		}
		cols := strings.Split(line, ",")
		if len(cols) < 7 {
			t.Fatalf("line %d: want >=7 columns, got %d: %q", lineNo, len(cols), line)
		}
		dev, err := strconv.ParseUint(strings.TrimSpace(cols[1]), 10, 64)
		if err != nil {
			t.Fatalf("line %d: bad device_id %q: %v", lineNo, cols[1], err)
		}
		if uint(dev) != deviceID {
			continue
		}
		ts, err := parseRealTS(cols[6])
		if err != nil {
			t.Fatalf("line %d: bad timestamp %q: %v", lineNo, cols[6], err)
		}
		if ts.Before(from) || !ts.Before(to) {
			continue
		}
		name := strings.TrimSpace(cols[2])
		raw := strings.TrimSpace(cols[3])
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("line %d: bad value %q for %s: %v", lineNo, raw, name, err)
		}
		key := ts.UnixNano()
		a := byTS[key]
		if a == nil {
			a = &acc{ts: ts}
			byTS[key] = a
			order = append(order, key)
		}
		a.read = append(a.read, Reading{Name: name, Value: v})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan realdata: %v", err)
	}

	frames := make([]realFrame, 0, len(order))
	for _, k := range order {
		a := byTS[k]
		// 帧内字段顺序固定为字段名字典序 —— 这正是 slot 的位流顺序，
		// 让"同一帧类型"在排序上稳定可复现。
		sort.Slice(a.read, func(i, j int) bool { return a.read[i].Name < a.read[j].Name })
		frames = append(frames, realFrame{DeviceID: deviceID, TS: a.ts, Readings: a.read})
	}
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].TS.Before(frames[j].TS) })
	if maxFrames > 0 && len(frames) > maxFrames {
		frames = frames[:maxFrames]
	}
	if len(frames) == 0 {
		t.Fatalf("no frames for device %d in [%s, %s)", deviceID, from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	return frames
}

// parseRealTS 解析生产导出的 timestamptz 文本。
func parseRealTS(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range realdataTSLayouts {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("no layout matched")
}
