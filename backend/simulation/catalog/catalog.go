//go:build simulation

// Package catalog 是场景目录：每个场景自注册一个 Scenario，
// simulation_test.go 顺序遍历 catalog.All() 执行，并由 TestCatalogGate
// 对目录本身做结构门禁（设计 §5.4 / §5.5）。
//
// 拆成多个文件（dep.go / auth.go / node.go / chan.go / ...）是为了让
// 不同域的并行实现互不冲突：注册表本身是包级共享状态，Register 由
// init() 调用，因此新增一个域只需新增一个文件。
package catalog

import (
	"regexp"
	"sort"

	"ehome/backend/simulation/harness"
)

// Domain 是场景域。取值是设计 §6 表"前缀"列去掉 SIM- 后的短名。
//
// 不变量（TestCatalogGate 第 6 条）：场景 ID 必须是
// "SIM-" + string(Domain) + "-" + 三位数字。
type Domain string

// 全部合法域（设计 §6 表，共 13 个 + 自动化专章 §6 新增 11 个子域 = 24 个）。
//
// 为什么自动化要拆成 11 个子域：用户明确要求「50+ 场景针对自动化引擎」，
// 而门禁 5 要求每个域 ≥3 个场景 —— 把 60 个自动化场景塞进单个 AUTO 域会让
// 域粒度失去意义（无法按子域跑、无法看出覆盖缺口）。拆域后每个子域对应引擎的一条语义轴。
// 详见 docs/设计/自动化引擎场景仿真验证.md §4。
const (
	DomainDEP   Domain = "DEP"   // 部署与初始化
	DomainAUTH  Domain = "AUTH"  // 认证与会话
	DomainNODE  Domain = "NODE"  // 节点接入与在线状态
	DomainCHAN  Domain = "CHAN"  // 通道与配置清单
	DomainEDGE  Domain = "EDGE"  // 边缘设备与逻辑设备
	DomainDATA  Domain = "DATA"  // 数据采集链路
	DomainDS    Domain = "DS"    // 数据源主备与故障转移
	DomainAUTO  Domain = "AUTO"  // 自动化策略（基础 CRUD/触发/抑制/确认/手动）
	DomainALERT Domain = "ALERT" // 阈值告警与通知
	DomainCMD   Domain = "CMD"   // 指令下发与审计
	DomainOTA   Domain = "OTA"   // 固件升级
	DomainRT    Domain = "RT"    // 实时推送与运行观测
	DomainERR   Domain = "ERR"   // 错误语义与接口契约

	// ── 自动化引擎专项子域（自动化专章 §4）──
	DomainTRIG Domain = "TRIG" // 触发语义（比较符/持续时间/目标限定）
	DomainCOND Domain = "COND" // 附加条件（AND/缺失/非法配置/执行前复核）
	DomainWIND Domain = "WIND" // 时间窗口（enter/exit/inside/跨夜）
	DomainDBLN Domain = "DBLN" // 防抖与限流（冷却/同窗节流/日熔断）
	DomainCNFM Domain = "CNFM" // 确认制闭环（待确认/确认/幂等/近认证/过期）
	DomainACTN Domain = "ACTN" // 动作分发（通知/设备动作/审计/幂等键/门禁）
	DomainMANU Domain = "MANU" // 手动触发（跳过条件/仍受冷却与熔断）
	DomainCRUD Domain = "CRUD" // 规则管理（创建/失效/启停/校验/软删）
	DomainSCNE Domain = "SCNE" // 真实业务剧本（光照/光伏-BMS/雨量/无人值守）
	DomainAUDT Domain = "AUDT" // 审计与可观测（结果码/回链/触发值/指标）
	DomainNTFY Domain = "NTFY" // 通知送达（级别来源/未读/去重）
)

// KnownDomains 是门禁使用的合法域集合。
var KnownDomains = []Domain{
	DomainDEP, DomainAUTH, DomainNODE, DomainCHAN, DomainEDGE, DomainDATA,
	DomainDS, DomainAUTO, DomainALERT, DomainCMD, DomainOTA, DomainRT, DomainERR,
	DomainTRIG, DomainCOND, DomainWIND, DomainDBLN, DomainCNFM, DomainACTN,
	DomainMANU, DomainCRUD, DomainSCNE, DomainAUDT, DomainNTFY,
}

// IsKnownDomain 报告域是否在冻结的取值集合之内。
func IsKnownDomain(domain Domain) bool {
	for _, known := range KnownDomains {
		if known == domain {
			return true
		}
	}
	return false
}

// Scenario 是目录中的一条场景（设计 §5.4 冻结）。
//
// 禁止占位场景：Title / Doc / Run 任一为空都会被 TestCatalogGate 判定失败。
// Title 必须是"真实用户口吻"的一句话，不写实现细节。
type Scenario struct {
	ID     string // SIM-<DOMAIN>-<NNN>，全局唯一
	Title  string // 中文，真实用户视角
	Domain Domain // 见 §6 表
	Doc    string // 设计依据：关联的 docs/设计 文件
	Run    func(*harness.Env)
}

// 门禁阈值（设计 §5.5）。**不得为了通过而放宽**：
// 目录设计 72 个场景，下限 50 是为个别不可行项留的余量。
const (
	MinScenarios = 50 // 场景总数下限
	MinDomains   = 10 // 域数量下限
	MinPerDomain = 3  // 任一域的场数下限
)

// scenarioIDPattern 是设计 §5.5 第 2 条冻结的 ID 形态。
var scenarioIDPattern = regexp.MustCompile("^SIM-[A-Z]+-\\d{3}$")

// ScenarioIDPattern 暴露给门禁与测试入口做 ID 校验。
func ScenarioIDPattern() *regexp.Regexp { return scenarioIDPattern }

// registry 是包级注册表。init() 顺序在 Go 里按文件名排序，
// 因此 All() 显式按 ID 排序，使执行顺序与文件布局无关、完全确定。
var registry []Scenario

// Register 由各域文件的 init() 调用，登记一个场景。
//
// 这里刻意**不做**重复 ID 检查：重复必须由 TestCatalogGate 报出来
// （设计 §5.5 第 2 条要求门禁"校验并失败于 ID 重复"），
// 而不是让进程在 init 阶段 panic 掉，连门禁本身都跑不到。
func Register(scenario Scenario) {
	registry = append(registry, scenario)
}

// All 返回全部已注册场景，按 ID 升序排列（确定性：执行顺序不依赖文件布局）。
func All() []Scenario {
	scenarios := make([]Scenario, len(registry))
	copy(scenarios, registry)
	sort.SliceStable(scenarios, func(i, j int) bool { return scenarios[i].ID < scenarios[j].ID })
	return scenarios
}

// ---------- 跨域共用助手 ----------
//
// 按设计 §4.1 的包级命名规则，域文件只能声明带本域前缀的标识符；
// 被多个域使用的助手统一放在本文件，使用 sim 前缀。

// simLoginAttempt 发一次登录请求并原样返回响应（不做任何断言）：
// 有些场景恰恰要证明"这次登录必须失败"。
func simLoginAttempt(e *harness.Env, username, password string, rememberMe bool) *harness.Response {
	return e.NewSession().Post("/api/v1/auth/login", map[string]any{
		"username":   username,
		"password":   password,
		"rememberMe": rememberMe,
	})
}

// simCountRows 执行一次只读计数查询（设计 §3 原则 2：用户界面看不到的
// 持久化事实只能直连数据库断言）。失败即终止场景：计数查不出来时，
// 后续的"行数不变"断言会退化成永远为真的空断言。
func simCountRows(e *harness.Env, query string, args ...any) int64 {
	var count int64
	if err := e.SQL().QueryRow(query, args...).Scan(&count); err != nil {
		e.Fatalf("计数查询失败 (%s): %v", query, err)
	}
	return count
}

// DomainOf 从场景 ID 中解析域段（"SIM-NODE-003" → "NODE"）。
// 解析失败返回空串，由门禁判定失败。
func DomainOf(id string) string {
	matches := scenarioIDPattern.FindStringSubmatch(id)
	if matches == nil {
		return ""
	}
	// ID 形态固定为 SIM-<DOMAIN>-<NNN>，取中段。
	for i := len("SIM-"); i < len(id); i++ {
		if id[i] == '-' {
			return id[len("SIM-"):i]
		}
	}
	return ""
}
