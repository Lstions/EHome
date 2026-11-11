package main

// maintenance_gate_test.go — 启动期时序的**源码级结构门禁**。
//
// 为什么需要它：本任务的核心价值是「监听提前 + 迁移期间可见进度」，而这完全由
// main.go 里几行的**相对顺序**决定。任何一次「顺手整理」把 ListenAndServe 挪回
// database.Connect 之后，或者把 SetEngine 挪到 SetupRoutes 之前，功能就静默失效，
// 而且**所有单元测试照样全绿**（没有任何测试会真的跑一遍 40 分钟的迁移）。
//
// 这是本仓既有的门禁范式（见 cmd/server/field_resolver_test.go 的
// TestFieldResolverWiringGate、notify_wiring_gate_test.go）：读生产源码，断言
// 关键片段存在且相对顺序正确。它防的是**无意的重构**，不证明运行时行为
// （运行时的 HTTP 契约由 internal/api/maintenance_test.go 覆盖）。
//
// 范围声明（诚实边界）：源码扫描无法识别「把调用写在注释里」这类规避，也看不见
// 跨文件的等价改写；它守的是「顺序被无意改动」这一个具体回归。

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// maintenanceGateMainFile 是被检的生产文件（本包目录下）。
const maintenanceGateMainFile = "main.go"

// readStartupMainSource 读取 main.go 源码（读不到即失败：门禁不能静默失效）。
//
// 名字刻意与 notify_wiring_gate_test.go 的 readMainSource 区分：同包内重名会编译失败
// （本文件与既有门禁并存，且两者都在同一个 package main 里）。
//
// ⚠ 返回的是**去掉注释后**的源码。为什么必须去注释：本文件断言的片段
// （rootHandler.SetEngine(r) / api.SetupRoutes( / srv.ListenAndServe()）也会出现在
// **解释性注释**里（例如「装配完成后 rootHandler.SetEngine(r) 一次性原子切换」）。
// 直接 strings.Index 会命中注释里的那一次，把顺序判反 —— 这条门禁第一版就是这么红的，
// 是个真实教训：源码门禁必须只扫描代码，不扫描散文。
func readStartupMainSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(maintenanceGateMainFile)
	if err != nil {
		t.Fatalf("read %s: %v", maintenanceGateMainFile, err)
	}
	return stripGoComments(string(raw))
}

// stripGoComments 去掉 Go 源码里的行注释与块注释，保留字符串/字符字面量原样。
//
// 为什么不能简单按 "//" 切行：字符串里会出现 "https://..." 这类内容
// （main.go 的 OTA/下载 URL 就有），粗暴切分会把代码切坏、让门禁的匹配位置漂移。
// 因此做一次最小状态机扫描：普通代码 / 字符串 / raw 字符串 / rune / 行注释 / 块注释。
//
// 注释字符被替换成空格而不是删除：保持下标与原文件一致，失败信息里的下标
// 仍能直接对应编辑器行位置（便于定位）。
func stripGoComments(src string) string {
	out := []byte(src)
	const (
		stCode = iota
		stLineComment
		stBlockComment
		stString
		stRawString
		stRune
	)
	state := stCode
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case stCode:
			switch {
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				state = stLineComment
				out[i] = ' '
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = stBlockComment
				out[i] = ' '
			case c == '"':
				state = stString
			case c == '`': // 反引号 raw string
				state = stRawString
			case c == '\'':
				state = stRune
			}
		case stLineComment:
			if c == '\n' {
				state = stCode
				continue
			}
			out[i] = ' '
		case stBlockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				out[i] = ' '
				out[i+1] = ' '
				i++
				state = stCode
				continue
			}
			if c != '\n' {
				out[i] = ' '
			}
		case stString:
			if c == '\\' && i+1 < len(src) {
				i++ // 跳过转义的下一个字符（含 \" ）
				continue
			}
			if c == '"' {
				state = stCode
			}
		case stRawString:
			if c == '`' {
				state = stCode
			}
		case stRune:
			if c == '\\' && i+1 < len(src) {
				i++
				continue
			}
			if c == '\'' {
				state = stCode
			}
		}
	}
	return string(out)
}

// mustIndex 返回 needle 在源码中的下标；缺失即 t.Fatalf（顺序断言的前提）。
func mustIndex(t *testing.T, src, needle string) int {
	t.Helper()
	i, err := indexOf(src, needle)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return i
}

// indexOf 是 mustIndex 的纯函数版本（返回 error 而不是 t.Fatalf），
// 供变异自检直接调用判定逻辑。
func indexOf(src, needle string) (int, error) {
	i := strings.Index(src, needle)
	if i < 0 {
		return -1, fmt.Errorf("main.go 找不到关键片段 %q（接线被删除或重命名）", needle)
	}
	return i, nil
}

// TestStartupListenBeforeDatabaseConnect 是本任务最核心的一条门禁：
// http.Server 必须在 database.Connect **之前**就 ListenAndServe。
//
// 若这条被破坏，改造就退回到「端口根本没监听」的原状：用户升级期间只能看到
// 连接失败/网关超时，维护页永远不可能出现（它自己都连不上）。
func TestStartupListenBeforeDatabaseConnect(t *testing.T) {
	src := readStartupMainSource(t)
	if err := checkListenBeforeConnect(src); err != nil {
		t.Fatalf("%v", err)
	}
}

// checkListenBeforeConnect 是真正的判定逻辑（从测试里抽出来，供变异自检重放）。
//
// 抽出的理由见 TestStartupGate_MutationSelfCheck：一个"永远返回 nil"的坏扫描器
// 会让门禁永远绿（vacuous pass），本仓已有此教训
// （datalifecycle/rollup_retirement_gate_test.go）。只有把判定抽成纯函数，
// 才能喂"错误的源码"进去、要求它必须判红。
func checkListenBeforeConnect(src string) error {
	listen, err := indexOf(src, "srv.ListenAndServe()")
	if err != nil {
		return err
	}
	connect, err := indexOf(src, "database.Connect(database.Config{")
	if err != nil {
		return err
	}
	if listen > connect {
		return fmt.Errorf("启动时序被破坏: ListenAndServe (下标 %d) 必须在 database.Connect (下标 %d) **之前**；"+
			"当前顺序相反 ⇒ 迁移/索引 DDL 期间端口不监听，运维看不到维护页", listen, connect)
	}

	// 维护引擎与 RootHandler 必须同样早于数据库连接 —— 监听时有东西可服务。
	maintenanceEngine, err := indexOf(src, "api.NewMaintenanceEngine(")
	if err != nil {
		return err
	}
	rootHandler, err := indexOf(src, "api.NewRootHandler(maintenanceEngine)")
	if err != nil {
		return err
	}
	if maintenanceEngine > connect || rootHandler > connect {
		return fmt.Errorf("维护引擎(%d)/RootHandler(%d) 必须早于 database.Connect(%d) 构造",
			maintenanceEngine, rootHandler, connect)
	}

	// 进度状态机必须最先建：后面的每一步都要往里记账。
	tracker, err := indexOf(src, "bootstrap.NewTracker(bootstrap.DefaultSteps())")
	if err != nil {
		return err
	}
	if tracker > connect {
		return fmt.Errorf("startup tracker(%d) 必须早于 database.Connect(%d)", tracker, connect)
	}
	return nil
}

// TestStartupEngineSwitchAfterRoutes 门禁：原子切换必须发生在**全部路由注册之后**。
//
// 早一步 SetEngine ⇒ 用户会看到 404（业务路由还没注册）；
// 晚一步 ⇒ 装配完毕却仍显示维护页，看起来像卡死。
// 因此断言 SetEngine 在 api.SetupRoutes 与 setupStaticRoutes 两者之后。
func TestStartupEngineSwitchAfterRoutes(t *testing.T) {
	src := readStartupMainSource(t)
	if err := checkSwitchAfterRoutes(src); err != nil {
		t.Fatalf("%v", err)
	}
}

// checkSwitchAfterRoutes 是切换时机的判定逻辑（同样抽出来供变异自检重放）。
func checkSwitchAfterRoutes(src string) error {
	setEngine, err := indexOf(src, "rootHandler.SetEngine(r)")
	if err != nil {
		return err
	}
	setupRoutes, err := indexOf(src, "api.SetupRoutes(")
	if err != nil {
		return err
	}
	staticRoutes, err := indexOf(src, "setupStaticRoutes(r, staticDir)")
	if err != nil {
		return err
	}
	if setEngine < setupRoutes {
		return fmt.Errorf("切换时机错误: rootHandler.SetEngine (下标 %d) 必须在 api.SetupRoutes (下标 %d) **之后**；"+
			"路由尚未注册完就切换 ⇒ 用户看到 404", setEngine, setupRoutes)
	}
	if setEngine < staticRoutes {
		return fmt.Errorf("切换时机错误: rootHandler.SetEngine (下标 %d) 必须在 setupStaticRoutes (下标 %d) **之后**", setEngine, staticRoutes)
	}
	// 反向断言：切换是单向的，生产代码不得把已就绪服务打回维护态。
	if strings.Contains(src, "rootHandler.SetEngine(nil)") {
		return fmt.Errorf("生产代码不得把已就绪服务打回维护态（SetEngine(nil) 是单向切换的反例）")
	}
	return nil
}

// TestStartupGate_MutationSelfCheck 是扫描器的**变异自检**：把"错误顺序"的真实写法
// 喂给判定逻辑，要求逐条判红。
//
// 为什么必须有（本仓教训）：一个写坏的门禁（比较方向写反、或提前 return nil）
// 会让所有断言永远通过 —— 门禁在，但毫无作用，且没人会发现。这里用 5 个变异体
// 反证扫描器真的会红：
//  1. 监听挪到 database.Connect 之后；
//  2. 维护引擎挪到 Connect 之后；
//  3. SetEngine 挪到 SetupRoutes 之前；
//  4. 生产代码出现 SetEngine(nil)（回切维护态）；
//  5. 关键片段被删（必须报错，不能静默通过）；
//  6. 注释里的同名片段不得被当成代码（本门禁第一版就栽在这里）。
func TestStartupGate_MutationSelfCheck(t *testing.T) {
	correct := joinSource(
		"bootstrap.NewTracker(bootstrap.DefaultSteps())",
		"api.NewMaintenanceEngine(",
		"api.NewRootHandler(maintenanceEngine)",
		"srv.ListenAndServe()",
		"database.Connect(database.Config{",
		"api.SetupRoutes(",
		"setupStaticRoutes(r, staticDir)",
		"rootHandler.SetEngine(r)",
	)
	if err := checkListenBeforeConnect(correct); err != nil {
		t.Fatalf("自检基线错误：正确顺序被判红: %v", err)
	}
	if err := checkSwitchAfterRoutes(correct); err != nil {
		t.Fatalf("自检基线错误：正确顺序被判红: %v", err)
	}

	listenAfterConnect := joinSource(
		"bootstrap.NewTracker(bootstrap.DefaultSteps())",
		"api.NewMaintenanceEngine(",
		"api.NewRootHandler(maintenanceEngine)",
		"database.Connect(database.Config{",
		"srv.ListenAndServe()",
		"api.SetupRoutes(",
		"setupStaticRoutes(r, staticDir)",
		"rootHandler.SetEngine(r)",
	)
	if err := checkListenBeforeConnect(listenAfterConnect); err == nil {
		t.Fatal("变异 1 未被抓住：ListenAndServe 在 Connect 之后，门禁却判绿（扫描器失效）")
	}

	engineAfterConnect := joinSource(
		"bootstrap.NewTracker(bootstrap.DefaultSteps())",
		"srv.ListenAndServe()",
		"database.Connect(database.Config{",
		"api.NewMaintenanceEngine(",
		"api.NewRootHandler(maintenanceEngine)",
		"api.SetupRoutes(",
		"setupStaticRoutes(r, staticDir)",
		"rootHandler.SetEngine(r)",
	)
	if err := checkListenBeforeConnect(engineAfterConnect); err == nil {
		t.Fatal("变异 2 未被抓住：维护引擎在 Connect 之后构造，门禁却判绿")
	}

	switchBeforeRoutes := joinSource(
		"bootstrap.NewTracker(bootstrap.DefaultSteps())",
		"api.NewMaintenanceEngine(",
		"api.NewRootHandler(maintenanceEngine)",
		"srv.ListenAndServe()",
		"database.Connect(database.Config{",
		"rootHandler.SetEngine(r)",
		"api.SetupRoutes(",
		"setupStaticRoutes(r, staticDir)",
	)
	if err := checkSwitchAfterRoutes(switchBeforeRoutes); err == nil {
		t.Fatal("变异 3 未被抓住：SetEngine 在 SetupRoutes 之前，门禁却判绿")
	}

	rollback := correct + "\n\trootHandler.SetEngine(nil)\n"
	if err := checkSwitchAfterRoutes(rollback); err == nil {
		t.Fatal("变异 4 未被抓住：出现 SetEngine(nil) 回切维护态，门禁却判绿")
	}

	if err := checkListenBeforeConnect(joinSource("srv.ListenAndServe()")); err == nil {
		t.Fatal("变异 5 未被抓住：database.Connect 消失时门禁却判绿")
	}

	commented := stripGoComments(joinSource(
		"// 装配完成后 rootHandler.SetEngine(r) 一次性切换",
		"// 在 database.Connect 之前 srv.ListenAndServe()",
		"bootstrap.NewTracker(bootstrap.DefaultSteps())",
		"api.NewMaintenanceEngine(",
		"api.NewRootHandler(maintenanceEngine)",
		"srv.ListenAndServe()",
		"database.Connect(database.Config{",
		"api.SetupRoutes(",
		"setupStaticRoutes(r, staticDir)",
		"rootHandler.SetEngine(r)",
	))
	if err := checkListenBeforeConnect(commented); err != nil {
		t.Fatalf("注释剥离失效：注释里的同名片段被当成了代码: %v", err)
	}
}

// joinSource 用换行拼接若干源码片段（构造变异体用）。
func joinSource(lines ...string) string {
	return strings.Join(lines, "\n")
}

// TestStartupFailureVisibleBeforeFatal 门禁：Fatal 之前必须先把失败记进进度。
//
// 否则进程直接退出，浏览器只看到连接断开 —— 维护页拿不到 failed 快照，
// 用户永远不知道「是连不上数据库」还是「索引建失败了」。
func TestStartupFailureVisibleBeforeFatal(t *testing.T) {
	src := readStartupMainSource(t)

	// 每一对 (Fail, Fatal) 都必须 Fail 在前。这里逐个检查真正的致命路径。
	pairs := []struct {
		name  string
		fail  string
		fatal string
	}{
		{
			name:  "db_connect",
			fail:  "startupTracker.Fail(bootstrap.StepDBConnect, err)",
			fatal: "Failed to connect database",
		},
		{
			name:  "auto_migrate",
			fail:  "startupTracker.Fail(bootstrap.StepAutoMigrate, err)",
			fatal: "Failed to migrate database",
		},
		// 2026-11-08 硬切换移除 partition_unified_data 这条致命路径：
		// main.go 不再调 MigrateUnifiedDataToPartitioned（否则 DROP 后重启会把空表重建）。
		//
		// 2026-11-11 device_data 整表退役（原始数据退役方案 §2.1），它的致命路径同样
		// 随之删除 —— 表都不存在了，没有"迁移失败"这回事。
		// 剩下的分区迁移致命路径见下面 telemetry_frame（L2 主写入路径，缺表 = 静默丢帧）。
		{
			name:  "partition_telemetry_frame",
			fail:  "startupTracker.Fail(bootstrap.StepPartitionTelemetryFrame, err)",
			fatal: "telemetry_frame partition migration failed",
		},
	}
	for _, p := range pairs {
		failIdx := mustIndex(t, src, p.fail)
		fatalIdx := mustIndex(t, src, p.fatal)
		if failIdx > fatalIdx {
			t.Errorf("%s: tracker.Fail (下标 %d) 必须在 Fatal (下标 %d) 之前 —— "+
				"否则维护页看不到失败原因，用户只看到连接断开", p.name, failIdx, fatalIdx)
		}
	}

	// 致命路径必须走统一出口 abortStartup（内含「留窗口再 Fatal」）。
	if !strings.Contains(src, "func abortStartup(") {
		t.Fatalf("main.go 缺少 abortStartup：致命失败必须先留窗口让维护页抓到 failed 快照")
	}
	if got := strings.Count(src, "abortStartup(\""); got < len(pairs) {
		t.Errorf("abortStartup 调用点 %d 个，少于致命路径 %d 条 ⇒ 有路径绕过了失败可见性", got, len(pairs))
	}
}

// TestStartupAllStepsMarked 门禁：契约 §3.1 的 10 个步骤在 main.go 里都要有终态记账。
//
// 漏标任何一步 ⇒ tracker.Ready() 永假 ⇒ /readyz 永远 503 ⇒ 部署永远卡在维护页。
// 这是最容易犯、后果最严重的一类遗漏（新加一个 step 时只加了 Begin 忘了 Done）。
func TestStartupAllStepsMarked(t *testing.T) {
	src := readStartupMainSource(t)
	steps := []string{
		bootstrapStepDBConnect,
		bootstrapStepAutoMigrate,
		// 2026-11-11: bootstrapStepPartitionDeviceData 已删除（device_data 整表退役）。
		bootstrapStepPartitionTelemetryFrame,
		bootstrapStepPartitionRollup,
		bootstrapStepEnsurePartitions,
		bootstrapStepParentIndexes,
		bootstrapStepRetireDeadIndexes,
		bootstrapStepWarmup,
		bootstrapStepStartupTasks,
	}
	for _, step := range steps {
		// 每一步至少要出现一次「终态记账」（Done 或 Skip）。
		if !strings.Contains(src, "startupTracker.Done("+step) &&
			!strings.Contains(src, "startupTracker.Skip("+step) {
			t.Errorf("步骤 %q 没有任何 Done/Skip 记账 ⇒ tracker 永不 ready ⇒ /readyz 永远 503", step)
		}
	}
}

// bootstrap 步骤常量名在源码里的字面量（避免本测试 import bootstrap 只为拿字符串：
// 门禁要检的正是 main.go 里写的那个符号）。
const (
	bootstrapStepDBConnect               = "bootstrap.StepDBConnect"
	bootstrapStepAutoMigrate             = "bootstrap.StepAutoMigrate"
	bootstrapStepPartitionRollup         = "bootstrap.StepPartitionRollup"
	bootstrapStepPartitionTelemetryFrame = "bootstrap.StepPartitionTelemetryFrame"
	bootstrapStepEnsurePartitions        = "bootstrap.StepEnsurePartitions"
	bootstrapStepParentIndexes           = "bootstrap.StepParentIndexes"
	bootstrapStepRetireDeadIndexes       = "bootstrap.StepRetireDeadIndexes"
	bootstrapStepWarmup                  = "bootstrap.StepWarmup"
	bootstrapStepStartupTasks            = "bootstrap.StepStartupTasks"
)

// TestStartupMigrationProgressWired 门禁：datalifecycle 的细粒度埋点必须接到 tracker。
//
// parent_indexes / retire_dead_indexes 嵌在迁移函数内部，main 无法直接 Begin/Done，
// 全靠 SetMigrationProgress 回调驱动。这条接线被删 ⇒ 生产上最耗时的两步没有进度，
// 维护页在几分钟内一动不动（比没有维护页更糟：用户以为卡死了）。
func TestStartupMigrationProgressWired(t *testing.T) {
	src := readStartupMainSource(t)
	for _, needle := range []string{
		"datalifecycle.SetMigrationProgress(progressAdapter)",
		"func (a *startupProgressAdapter) StepProgress(stepID string, pct float64, detail string)",
		"progressAdapter.wasTouched(bootstrap.StepParentIndexes)",
		"progressAdapter.wasTouched(bootstrap.StepRetireDeadIndexes)",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("main.go 缺少迁移进度接线片段: %q", needle)
		}
	}
}

// TestStartupMaintenancePageEnvSwitch 门禁：排障开关必须存在且默认 true。
//
// EHOME_STARTUP_MAINTENANCE_PAGE=false 是契约 §3.4/§5 规定的一键回退路径
// （排障时用来隔离「提前监听」这个变量）。默认值必须为 true —— 否则整个改造
// 默认不生效，等于白做。
func TestStartupMaintenancePageEnvSwitch(t *testing.T) {
	src := readStartupMainSource(t)
	if !strings.Contains(src, "EHOME_STARTUP_MAINTENANCE_PAGE") {
		t.Fatalf("main.go 缺少 EHOME_STARTUP_MAINTENANCE_PAGE 开关（契约 §3.4 的一键回退路径）")
	}
	if !strings.Contains(src, "func startupMaintenancePageEnabled() bool") {
		t.Fatalf("main.go 缺少 startupMaintenancePageEnabled 实现")
	}
	// 默认 true：函数体里 raw 为空的分支必须 return true（而不是 false）。
	body := extractFunctionBody(t, src, "func startupMaintenancePageEnabled() bool {")
	if !strings.Contains(body, "raw == \"\"") {
		t.Errorf("startupMaintenancePageEnabled 缺少空值分支（默认值必须显式处理）:\n%s", body)
	}
	emptyIdx := strings.Index(body, "raw == \"\"")
	firstReturn := strings.Index(body[emptyIdx:], "return true")
	if emptyIdx < 0 || firstReturn < 0 {
		t.Errorf("startupMaintenancePageEnabled 默认值必须是 true（契约 §3.4）:\n%s", body)
	}
}

// extractFunctionBody 粗略截取一个函数体（用于对函数内容做断言）。
//
// 不够严谨（按首尾大括号配平），但对「这条分支是否存在」足够，且不引入
// go/parser 的复杂度 —— 本门禁是防无意重构，不是做 AST 分析。
func extractFunctionBody(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("找不到函数签名 %q", signature)
	}
	depth := 0
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("函数 %q 的大括号不配平（源码被截断？）", signature)
	return ""
}
