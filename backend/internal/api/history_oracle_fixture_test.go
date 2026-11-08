package api

// history_oracle_fixture_test.go — 用**冻结的生产数据**替代已删除的 SQL 对拍基准。
//
// ============ 这个文件为什么存在 ============
//
// 2026-11-08 决定 DROP unified_data。在此之前仓库里有两条**活的对拍**：
//
//	TestFrameSourceEqualsUnifiedData        帧表解码 == queryHistoryPoints（SQL 路径）
//	TestRollupMatchesUnifiedForLongWindows  rollup 重建 == queryHistoryPoints（SQL 路径）
//
// 它们把「生产 SQL 路径」当**独立基准**。表一旦 DROP，这两条不是变红，而是**跑不了** ——
// 而「跑不了」在 CI 里通常表现为 skip，**而 skip 是绿的**。
//
//	一个会静默变成「总是通过」的证明，比没有证明更危险。
//
// 所以在删代码**之前**，把 SQL 路径的输出冻结成 testdata/oracle_20261105.json。
// 之后代码全删，期望值以数据留下：生产代码里零 unified_data 读路径，
// 而对拍能力**永久保留且不再依赖任何表**。
//
// ============ 为什么 fixture 比「活的对拍」更强 ============
//
//	活的对拍：oracle 与被验对象在同一次 CI 里跑，两条路都改就可能一起变；
//	fixture：冻结的事实。解码改坏了它照样红 —— 而且你没法说「可能两边都改了」，
//	         因为一边是常量数据，根本没被改。
//
// ============ ⚠ 时代边界（写在最显眼的地方）============
//
// fixture 取 2026-11-05，即 **L1（值不变不入库）生效前**：那时 unified_data 每帧存
// 全部 29 个字段，与帧表结构上**应当逐点相等**。
//
// L1 于 2026-11-07 生效后，unified_data 只存**变化**字段（实测 29.00 → 3.47 行/帧）。
// 那时两条路**结构上不可能相等** —— 那不是 bug，是 L1 的定义。
//
// 所以：**不要用本 fixture 去论证 L1 之后的数据形态。**
// 要扩窗口，先回答：那个窗口里 unified_data 是不是全字段？
//
// ============ payload 是生产原样，不重新编码 ============
//
// 用例把 fixture 里的 base64 payload **直接**写进 telemetry_frame，不本地重编。
// 若重编一遍，验的就变成「编码器 ↔ 解码器自洽」—— 两边一起改错照样绿。
// 只有原样字节才能验「解码器 ↔ 生产写出去的东西」。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

const oracleFixturePath = "testdata/oracle_20261105.json"

type oracleFixtureFrame struct {
	TS        string `json:"ts"`
	Seq       int    `json:"seq"`
	FrameKind int    `json:"frame_kind"`
	SlotID    int    `json:"slot_id"`
	Payload   string `json:"payload_b64"`
}

type oracleFixturePoint struct {
	TS    string  `json:"ts"`
	Value float64 `json:"value"`
}

type oracleFixture struct {
	Note      []string                        `json:"_note"`
	DeviceID  uint                            `json:"device_id"`
	Start     string                          `json:"start"`
	End       string                          `json:"end"`
	MaxPoints int                             `json:"max_points"`
	Slot      []telemetry.SlotField           `json:"slot"`
	Frames    []oracleFixtureFrame            `json:"frames"`
	OracleAvg map[string][]oracleFixturePoint `json:"oracle_avg"`
	OracleChg map[string][]oracleFixturePoint `json:"oracle_change"`
	// OracleRollup 是**生产 telemetry_rollup_1m 的真实 payload**（base64）。
	//
	// 与上面两个 oracle 的区别：它们是从 unified_data 的 SQL 聚合导出的，
	// 而这份来自 rollup 表本身 —— 由 live collector（内存累加）或历史回填写入，
	// **两条路都不读帧表**。所以拿它验「帧表重建 rollup」是真正的跨路径对拍。
	//
	// 详见 rollup_oracle_fixture_test.go 的文件头（含为什么按日期筛而不按 source 列筛）。
	OracleRollup []oracleFixtureRollupMinute `json:"oracle_rollup_1m"`
}

// oracleFixtureRollupMinute 是一条 rollup 分钟行的原始 payload。
type oracleFixtureRollupMinute struct {
	Bucket  string `json:"bucket"`
	Payload string `json:"payload_b64"`
}

func loadOracleFixture(t *testing.T) *oracleFixture {
	t.Helper()
	raw, err := os.ReadFile(oracleFixturePath)
	if err != nil {
		t.Fatalf("读 oracle fixture: %v\n本文件由 backend/scripts/gen_oracle_fixture.py 生成（文件头有重新生成的步骤）", err)
	}
	var f oracleFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析 oracle fixture: %v", err)
	}
	if len(f.Frames) == 0 || len(f.OracleAvg) == 0 || len(f.OracleChg) == 0 {
		t.Fatal("oracle fixture 为空 —— 文件损坏或被误清（不要为了让它绿而改成 skip，那会把整个证明删掉）")
	}
	return &f
}

// buildFixtureDictionary 用 fixture 里的 slot 定义构造内存字典。
//
// 不走 DBSlots：那需要先建 telemetry_slot 表并写入，而 fixture 里的字段规格
// 就是生产当时的快照 —— 直接用它构造 MemorySlots 才是「所见即所得」。
// fixtureDictionary 是一个只含**一个** slot 的最小字典，slot ID 取自 fixture。
//
// 为什么不用 telemetry.NewMemorySlots：它从 1 开始自增分配 ID，而 fixture 里的帧行
// 用的是**生产当时的 slot_id（10）**。改帧行的 slot_id 不行 —— payload 的位流布局
// 与字段顺序都由那一版 slot 定义决定，改了就对不上；而伪造一个 ID 也不是我们想要的
// （我们要验的正是「生产写出去的那份字节」能被解回来）。
//
// 所以自己实现 SlotDictionary 的两个方法：解码路径只用到 ByID。
type fixtureDictionary struct {
	slot *telemetry.Slot
}

func (d fixtureDictionary) Resolve(int64, []string) (*telemetry.Slot, error) {
	return d.slot, nil
}

func (d fixtureDictionary) ByID(slotID int16) (*telemetry.Slot, bool) {
	if slotID == d.slot.ID {
		return d.slot, true
	}
	return nil, false
}

func (d fixtureDictionary) All() []*telemetry.Slot {
	return []*telemetry.Slot{d.slot}
}

func buildFixtureDictionary(t *testing.T, f *oracleFixture) telemetry.SlotDictionary {
	t.Helper()
	if len(f.Frames) == 0 || len(f.Slot) == 0 {
		t.Fatal("fixture 缺帧或缺 slot 定义")
	}
	// slot_id 以**帧行**为准（那是生产实际写入的值），字段顺序即位流顺序
	// （fixture 的 slot 数组按生产 fields_json 原样保存）。
	//
	// ⚠ 这里必须用 f.Slot 本身，不能 make(...,0,n) + copy —— 后者 len=0，copy 一个字节
	// 都拷不进去，于是 Slot.Fields 为空、DecodeFrame 报 "slot has no fields"，
	// 600 帧全被判 corrupt。我就是这么踩进去的，所以留这条注释。
	return fixtureDictionary{slot: &telemetry.Slot{
		ID:     int16(f.Frames[0].SlotID),
		Fields: f.Slot,
	}}
}

// seedOracleFrames 把 fixture 的真实生产 payload 写进 telemetry_frame。
func seedOracleFrames(t *testing.T, db *gorm.DB, f *oracleFixture) {
	t.Helper()
	rows := make([]models.TelemetryFrame, 0, len(f.Frames))
	for i := range f.Frames {
		ts, err := time.Parse(time.RFC3339Nano, f.Frames[i].TS)
		if err != nil {
			t.Fatalf("fixture 第 %d 帧时间解析失败: %v", i, err)
		}
		payload, err := base64.StdEncoding.DecodeString(f.Frames[i].Payload)
		if err != nil {
			t.Fatalf("fixture 第 %d 帧 payload 解码失败: %v", i, err)
		}
		rows = append(rows, models.TelemetryFrame{
			DeviceID: f.DeviceID, TS: ts.UTC(), Seq: int16(f.Frames[i].Seq),
			FrameKind: int16(f.Frames[i].FrameKind), SlotID: int16(f.Frames[i].SlotID),
			Payload: payload,
		})
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("写入 fixture 帧: %v", err)
	}
}

// setupOracleDB 建帧表并清空，返回可用连接。
func setupOracleDB(t *testing.T) *gorm.DB {
	t.Helper()
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM unified_data")
	return db
}

// fixtureWindow 返回 fixture 的 [start, end) 与 maxPoints。
func fixtureWindow(t *testing.T, f *oracleFixture) (time.Time, time.Time) {
	t.Helper()
	start, err := time.Parse(time.RFC3339Nano, f.Start)
	if err != nil {
		t.Fatalf("fixture start 解析失败: %v", err)
	}
	end, err := time.Parse(time.RFC3339Nano, f.End)
	if err != nil {
		t.Fatalf("fixture end 解析失败: %v", err)
	}
	return start, end
}

// comparePoints 逐点比对实际产出与冻结期望。
//
// 容差用 1e-9 而不是精确相等：Dequantize 的一次乘加会带来 1e-12 量级的浮点表示噪声。
// 1e-9 远小于任何 Step（最小 0.001），所以它只吸收表示噪声，不掩盖量化误差。
func comparePoints(t *testing.T, label string, got []HistoryPoint, want []oracleFixturePoint) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: 点数 %d != 冻结期望 %d\n实际前3: %s\n期望前3: %s",
			label, len(got), len(want), head3Points(got), head3Oracle(want))
	}
	var worst float64
	for i := range want {
		wantTS, err := time.Parse(time.RFC3339Nano, want[i].TS)
		if err != nil {
			t.Fatalf("%s: 期望第 %d 点时间解析失败: %v", label, i, err)
		}
		if !got[i].Timestamp.Equal(wantTS) {
			t.Fatalf("%s: 第 %d 点时间 %v != 期望 %v", label, i, got[i].Timestamp, wantTS)
		}
		if d := math.Abs(got[i].Value - want[i].Value); d > worst {
			worst = d
		}
	}
	if worst > 1e-9 {
		t.Fatalf("%s: 值最大偏差 %v > 1e-9（解码/聚合与冻结基准不符）", label, worst)
	}
	t.Logf("%s: %d 点全部相符，最大偏差 %v", label, len(got), worst)
}

func head3Points(ps []HistoryPoint) string {
	n := len(ps)
	if n > 3 {
		n = 3
	}
	out := ""
	for i := 0; i < n; i++ {
		out += ps[i].Timestamp.Format("15:04:05.000") + "=" + fmt.Sprintf("%.6f", ps[i].Value) + " "
	}
	return out
}

func head3Oracle(ps []oracleFixturePoint) string {
	n := len(ps)
	if n > 3 {
		n = 3
	}
	out := ""
	for i := 0; i < n; i++ {
		out += ps[i].TS + "=" + fmt.Sprintf("%.6f", ps[i].Value) + " "
	}
	return out
}

// sortedCategories 让遍历顺序稳定（map 迭代顺序随机会导致失败信息不可复现）。
func sortedCategories(m map[string][]oracleFixturePoint) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestFrameSourceMatchesOracleFixture_Avg 是**帧表解码 == 冻结生产基准**（avg 模式）。
//
// 它取代已删除的 TestFrameSourceEqualsUnifiedData —— 后者把 queryHistoryPoints（生产 SQL）
// 当独立基准，而那条 SQL 读 unified_data。表 DROP 后它只能 skip，而 skip 是绿的。
//
// 名字里的 20261105 是**基准来自哪一天**：看到它的人第一反应应该是
// 「这个日期为什么重要」→ 去看文件头（L1 生效前，两条路结构上应当相等）。
func TestFrameSourceMatchesOracleFixture_Avg(t *testing.T) {
	f := loadOracleFixture(t)
	db := setupOracleDB(t)
	dict := buildFixtureDictionary(t, f)
	seedOracleFrames(t, db, f)
	start, end := fixtureWindow(t, f)

	src := frameSource{db: db, dict: dict}
	for _, cat := range sortedCategories(f.OracleAvg) {
		got, err := src.Points(context.Background(), historyPointQuery{
			EdgeDeviceID: f.DeviceID, Category: cat,
			Start: start, End: end, MaxPoints: f.MaxPoints, Mode: AggModeAvg,
		})
		if err != nil {
			t.Fatalf("avg/%s 帧表路径失败: %v", cat, err)
		}
		comparePoints(t, "avg/"+cat, got, f.OracleAvg[cat])
	}
}

// TestFrameSourceMatchesOracleFixture_Change 是同一基准的 change 模式。
//
// change 语义比 avg 更容易写错：必须保留首点（prev IS NULL）和**两个方向**的跳变，
// 含 1→0 的脉冲结束点。fixture 里 rsoc 恒定（3）、protection_status 恒定（0）、
// fet_status 恒定（3），所以这三个 category 的 change 结果应该恰好是 1 个点 ——
// 那正是「恒定段只留首点」这条语义的天然断言。
func TestFrameSourceMatchesOracleFixture_Change(t *testing.T) {
	f := loadOracleFixture(t)
	db := setupOracleDB(t)
	dict := buildFixtureDictionary(t, f)
	seedOracleFrames(t, db, f)
	start, end := fixtureWindow(t, f)

	src := frameSource{db: db, dict: dict}
	for _, cat := range sortedCategories(f.OracleChg) {
		got, err := src.Points(context.Background(), historyPointQuery{
			EdgeDeviceID: f.DeviceID, Category: cat,
			Start: start, End: end, MaxPoints: f.MaxPoints, Mode: AggModeChange,
		})
		if err != nil {
			t.Fatalf("change/%s 帧表路径失败: %v", cat, err)
		}
		comparePoints(t, "change/"+cat, got, f.OracleChg[cat])
	}
}

// TestOracleFixture_CoversBothModesAndAllFields 是 fixture 的**自检**。
//
// 为什么需要：fixture 是数据，数据错了不会编译报错，用例也照样「通过」——
// 因为比对的是同样错的数据。这条用例检查 fixture 自身的形态：
//
//	· 两个模式覆盖的 category 集合必须相同（都是那 29 个字段）；
//	· 每个 category 的 avg 点数必须等于窗口内的桶数（W=10s, 600s => 60）；
//	· change 结果必须非空且首点时间 == 窗口内第一个采样时刻。
//
// 没有它的话，「fixture 生成脚本写错了一列」这种错误会静默传播到所有依赖它的用例。
func TestOracleFixture_CoversBothModesAndAllFields(t *testing.T) {
	f := loadOracleFixture(t)

	if len(f.OracleAvg) != len(f.OracleChg) {
		t.Fatalf("avg 覆盖 %d 个 category，change 覆盖 %d 个 —— 两个模式必须覆盖同一批字段",
			len(f.OracleAvg), len(f.OracleChg))
	}
	for cat := range f.OracleAvg {
		if _, ok := f.OracleChg[cat]; !ok {
			t.Fatalf("category %q 只在 avg 里，change 缺失", cat)
		}
	}

	// W = ceil(600s / 60) = 10s => 600s/10s = 60 个桶。
	const wantBuckets = 60
	for cat, pts := range f.OracleAvg {
		if len(pts) != wantBuckets {
			t.Fatalf("%s: avg 桶数 %d != %d（窗口 600s、W=10s；fixture 生成方式变了？）",
				cat, len(pts), wantBuckets)
		}
	}

	// change：恒定字段必须恰好 1 个点（只留首点）。
	for _, cat := range []string{"rsoc", "protection_status", "fet_status", "cycle_count", "nominal_capacity"} {
		pts, ok := f.OracleChg[cat]
		if !ok {
			continue // 该字段不在本 fixture 的 29 个里就跳过（不失败）
		}
		if len(pts) != 1 {
			t.Fatalf("%s 在窗口内是恒定值，change 结果应当恰好 1 个点（首点），实际 %d 个",
				cat, len(pts))
		}
	}

	// 首点时间必须等于**窗口内**第一个采样时刻。
	//
	// ⚠ fixture 的 frames 里包含一帧窗口**之前**的关键帧（锚点），
	// 否则窗口开头的增量帧没有基线，解码器按断链规则全部跳过 —— 那会让
	// 「帧表少 3 个桶」看起来像聚合 bug，实际是 fixture 缺锚点。
	// 所以这里要与「>= start 的第一帧」比，而不是 f.Frames[0]。
	start, _ := fixtureWindow(t, f)
	var firstInWindow string
	for _, fr := range f.Frames {
		ts, err := time.Parse(time.RFC3339Nano, fr.TS)
		if err != nil {
			t.Fatalf("帧时间解析失败: %v", err)
		}
		if !ts.Before(start) {
			firstInWindow = fr.TS
			break
		}
	}
	if firstInWindow == "" {
		t.Fatal("fixture 里没有落在窗口内的帧")
	}
	for cat, pts := range f.OracleChg {
		if len(pts) > 0 && pts[0].TS != firstInWindow {
			t.Fatalf("%s: change 首点 %s != 窗口首个采样 %s（prev IS NULL 必须保留首点）",
				cat, pts[0].TS, firstInWindow)
		}
	}

	// 锚点帧数：窗口之前的关键帧（解码起点），通常 1 帧。
	var anchorFrames int
	for _, fr := range f.Frames {
		ts, _ := time.Parse(time.RFC3339Nano, fr.TS)
		if ts.Before(start) {
			anchorFrames++
		}
	}
	if anchorFrames == 0 {
		t.Fatal("fixture 里没有窗口之前的关键帧锚点 —— 窗口开头的增量帧会因断链被跳过，" +
			"表现为「帧表少几个桶」，很容易被误判成聚合 bug")
	}

	t.Logf("fixture 自检通过：%d 帧（含 %d 帧锚点）/ %d 个字段 / avg %d 桶 × %d 字段 / change %d 点",
		len(f.Frames), anchorFrames, len(f.Slot), wantBuckets, len(f.OracleAvg), sumPoints(f.OracleChg))
}

func sumPoints(m map[string][]oracleFixturePoint) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}
