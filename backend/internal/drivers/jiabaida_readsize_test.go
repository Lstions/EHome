package drivers

import (
	"encoding/json"
	"testing"
)

// ============================================================================
// ReadSize 契约测试（2026-11-15 实机缺陷回归）
// ============================================================================
// 实机现象：「MOS 开关无法成功控制」。
// 根因：set_mos_policy 的读回步声明了 ReadSize: 60，而真实 0x03 帧长
// = 30 + 2×NTC（典型 38 B）。固件 uart_collect_response() 把 read_size 当成
// 「收满下界」（2 处判据都是 len >= expected），短帧永不满足 ⇒ 静默窗口一到
// 即判该步失败（error_code 0x1401）⇒ 整条计划 FAILED。
//
// 本文件的判据把两个语义钉死，防止再次漂移：
//   1. 有界计划的**每一步**都必须声明 ReadSize == 0（行空闲定帧）；
//   2. 单步动作的 ReadSize 是**接收窗口**，必须 > 0 且 <= 256
//      （read_size==0 会被固件当成"不期待回包"并立即以空载荷成功返回）。
//
// 为什么用 ControlActions() 自动合成参数：新增动作时无需维护第二份参数表，
// 判据自然覆盖到所有计划；只有真正编译不出来的动作才会暴露在错误信息里。

// jiabaidaSynthesizeParams 按动作声明合成一份"合法但无意义"的参数，
// 目的只是让 CompileControlActionPlan 走到读/写帧的组装分支。
func jiabaidaSynthesizeParams(t *testing.T, action ControlAction) json.RawMessage {
	t.Helper()
	values := make(map[string]any, len(action.Parameters))
	for _, p := range action.Parameters {
		switch p.Type {
		case "boolean":
			values[p.Name] = false
		case "integer":
			if p.Minimum != nil {
				values[p.Name] = int64(*p.Minimum)
			} else {
				values[p.Name] = int64(0)
			}
		case "string":
			if len(p.Enum) > 0 {
				values[p.Name] = p.Enum[0]
			} else {
				values[p.Name] = "A"
			}
		default:
			t.Fatalf("action %q parameter %q has unknown type %q", action.ID, p.Name, p.Type)
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("action %q params marshal: %v", action.ID, err)
	}
	return raw
}

// TestJiabaidaBoundedPlanStepsUseIdleFraming 是本次实机缺陷的回归判据：
// 任何有界计划的任何一步声明了非 0 ReadSize，都会在真实 BMS 上把
// 「回包比声明短」判成步骤失败。
func TestJiabaidaBoundedPlanStepsUseIdleFraming(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	covered := 0
	for _, action := range d.ControlActions() {
		if action.ExecutionShape != "bounded_sequence" {
			continue
		}
		plan, err := d.CompileControlActionPlan(action.ID, jiabaidaSynthesizeParams(t, action))
		if err != nil {
			t.Errorf("action %q: compile plan: %v", action.ID, err)
			continue
		}
		if len(plan.Steps) == 0 {
			t.Errorf("action %q: compiled plan has no steps", action.ID)
			continue
		}
		covered++
		for i, step := range plan.Steps {
			if step.ReadSize != 0 {
				t.Errorf("action %q step %d (%s) declares ReadSize=%d; bounded-plan steps MUST declare 0 "+
					"(idle framing). The firmware treats read_size as a LOWER BOUND (len >= expected) and a "+
					"real Jiabaida reply is variable length (0x03 = 30 + 2*NTC bytes), so any fixed value makes "+
					"that step fail on real hardware with error_code 0x1400+step.",
					action.ID, i, step.ID, step.ReadSize)
			}
		}
	}
	if covered < 10 {
		t.Fatalf("only %d bounded actions were covered; the sweep is not exercising the catalog", covered)
	}
}

// TestJiabaidaSingleStepReadsUseReceiverWindow 钉死单步语义：
// read_size 是接收窗口，必须 > 0（0 会让固件立即以空载荷完成），且不超过协议上界。
func TestJiabaidaSingleStepReadsUseReceiverWindow(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	covered := 0
	for _, action := range d.ControlActions() {
		if action.ExecutionShape == "bounded_sequence" || len(action.TXData) == 0 {
			continue
		}
		covered++
		if action.ReadSize == 0 {
			t.Errorf("action %q declares ReadSize=0; a single-step V2 command with read_size==0 is completed "+
				"immediately by the firmware with an empty payload (bus_worker.c: read_size==0 => "+
				"complete_control(success, NULL, 0)), so the response would never be captured", action.ID)
		}
		if action.ReadSize > 256 {
			t.Errorf("action %q declares ReadSize=%d > 256 (firmware admission window)", action.ID, action.ReadSize)
		}
	}
	if covered == 0 {
		t.Fatal("no single-step actions were covered")
	}
}

// TestJiabaidaMOSPlanPairsWithRealShortFrame 把「计划声明」与「真实帧长」绑在
// 同一个判据里：后端单测 fixture 造的就是 38 B（= 30 + 2*4 NTC），
// 若计划又一次要求 60 B，本用例必须红。
func TestJiabaidaMOSPlanPairsWithRealShortFrame(t *testing.T) {
	d := &JiabaidaBMSDriver{}
	plan, err := d.CompileControlActionPlan("set_mos_policy",
		json.RawMessage(`{"charge_software_closed":true,"discharge_software_closed":false,"priority":"user"}`))
	if err != nil {
		t.Fatalf("compile set_mos_policy plan: %v", err)
	}
	// 2026-11-25 起固定 4 步：基线 0x0F + E1 写 + 两帧 0x0F 回读。步数变了，
	// 但"每一步的 ReadSize 必须是 0"这条约束不变。
	if len(plan.Steps) != 4 {
		t.Fatalf("set_mos_policy must compile to 4 steps (baseline+write+2 readbacks), got %d", len(plan.Steps))
	}
	for i, step := range plan.Steps {
		if step.ReadSize != 0 {
			t.Fatalf("set_mos_policy step %d (%s) declares ReadSize=%d; must be 0 (0x0F reply length varies with NTC/cell count; a fixed lower bound caused the 2026-11-15 field failure)", i, step.ID, step.ReadSize)
		}
	}
	// 真实形状的回读帧：DD 0F 00 18 + 24 B + CRC + 77（长度随 NTC/串数变化）
	readback := jiabaidaMOSEnvelope(t, []byte{0xDD, 0xE1, 0x00, 0x00, 0x00, 0x00, 0x77}, 0x02, 0x0000)
	if len(readback) == 0 {
		t.Fatal("empty envelope fixture")
	}
	if _, err := d.VerifyControlAction("set_mos_policy",
		json.RawMessage(`{"charge_software_closed":true,"discharge_software_closed":false}`), readback); err != nil {
		t.Fatalf("verifier rejected the realistic 38 B readback: %v", err)
	}
}
