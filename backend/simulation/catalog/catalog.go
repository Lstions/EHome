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
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

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

// simHead 截断长文本用于失败信息，避免刷屏。
//
// 为什么放在 catalog.go 而不是各域各写一份：失败信息必须一致地带上
// 「端点 + 原始 data 前缀」，否则同一个解析缺陷在不同域会呈现成不同形态的
// 噪音。历史上 auto.go / rt.go 各有一份等价实现（autoHead / rtHead），
// 本助手是它们的跨域收敛点。
func simHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}

// simEnvelopePage 是分页端点的 data 段形状（架构评估 P1.2 裁决：items + total）。
//
// 为什么用泛型：列表元素类型各域不同（autoEventRow / nodeDetail / edgeDeviceRow ...），
// 但信封外层形状完全一致。泛型让"形状"只有一处定义，下一个端点改形状时
// 只需改这里，而不是满地改解析调用点。
type simEnvelopePage[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

// simPageEnvelope 解析**已分页**端点的 data 段，返回完整信封（含 total）。
//
// 契约（不可放宽）：只接受 {"items":[...], ...} 信封，收到裸数组必须**显式失败**。
//
// 为什么刻意**不**同时兼容裸数组（这是本缺陷的加固核心）：
// 本仓库刚发生过一次真实事故 —— 端点从裸数组改为 {items,total} 信封后，
// 场景侧的 json.Unmarshal 仍按裸数组解析，失败信息被 Eventually 吞成
// 「等待超时」，于是大批场景变红而无人知道真正原因。若这里同时接受两种形状，
// 端点**悄悄改回裸数组**（或改成第三种形状）时会再次退化成静默：解析"成功"、
// 但 total / 分页语义已经丢失，断言照常通过却不再验证真实契约。
// 只接受信封 + 明确报错，才能让"形状漂移"在第一次运行时立刻炸响。
//
// 失败信息必须包含：端点路径、期望形状、真实错误、原始 data 前缀。
func simPageEnvelope[T any](r *harness.Response, path string) (*simEnvelopePage[T], error) {
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("GET %s 返回 %d（期望 200）: %s", path, r.Status, r.BodyString())
	}
	trimmed := strings.TrimSpace(string(r.Data))
	if strings.HasPrefix(trimmed, "[") {
		return nil, fmt.Errorf("GET %s 的 data 是**裸数组**，但该端点的契约是分页信封 "+
			"{items,total,page,page_size}。端点形状已漂移（或回退）——按契约只能解析信封，"+
			"不接受裸数组。请核对 handler 源码后同步本解析：data=%s", path, simHead(trimmed, 200))
	}
	var page simEnvelopePage[T]
	if err := json.Unmarshal(r.Data, &page); err != nil {
		return nil, fmt.Errorf("解析 GET %s 的分页信封失败: %w（期望 {items,total,page,page_size}，data=%s）",
			path, err, simHead(trimmed, 200))
	}
	if page.Items == nil {
		// items 缺失与 items 为空数组是两件事：前者是形状漂移（例如被改名为
		// 旧方言 list），后者是合法的空集。绝不用"空列表"兜底掩盖前者。
		return nil, fmt.Errorf("GET %s 的分页信封缺少 items 字段（可能是被改名为旧方言 list，或形状漂移）：data=%s",
			path, simHead(trimmed, 200))
	}
	return &page, nil
}

// simPageItems 解析**已分页**端点的 data 段，返回第一页的 items 与错误。
//
// 非致命版本：供 Eventually 的轮询闭包做条件使用（轮询期间"数据还没到"
// 是正常状态，不是断言失败）。契约与 simPageEnvelope 完全一致。
func simPageItems[T any](r *harness.Response, path string) ([]T, error) {
	page, err := simPageEnvelope[T](r, path)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// simPageAll 跟随 total 读**全部分页**，返回过滤后的完整集合。
//
// 为什么必须有它（而不是只读第一页）：a96afda5 / ffdec935 / ea9ce296 给这些端点
// 接上真分页后，服务端默认 page_size=20。场景断言的语义是"该过滤条件下的**全部**
// 事件/节点"（例如"停用期间没有新增事件"是与全量条数比较），只读第一页会把断言
// 悄悄缩小到最近 20 条 —— 那是比形状漂移更隐蔽的假绿：条数少时照样通过，条数多时
// 结论错误。更直接的一例：SIM-RT-003 拿 GET /nodes 的**全长**与 /overview 的总数比对，
// 一次完整运行有 100+ 个节点，只读 20 条必然误报。
//
// 停止条件：累计条数 >= total，或某页不足一页（total 在读取期间增长时的兜底）。
// 页数上限用于防止"边读边写"导致的不收敛；触顶即**显式失败**，绝不静默截断。
func simPageAll[T any](e *harness.Env, base, query string) ([]T, error) {
	e.T.Helper()
	const (
		pageSize = 200 // 服务端上限（handler 里 [1,200] 之外归 20）
		maxPages = 100 // 20000 条；触顶说明过滤条件没有收窄，属于场景自身的问题
	)
	separator := "?"
	if strings.Contains(query, "?") {
		separator = "&"
	}
	var all []T
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("%s%s%spage=%d&page_size=%d", base, query, separator, page, pageSize)
		envelope, err := simPageEnvelope[T](e.Admin.Get(path), path)
		if err != nil {
			return nil, err
		}
		all = append(all, envelope.Items...)
		if int64(len(all)) >= envelope.Total || len(envelope.Items) < pageSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("GET %s%s 的分页结果超过 %d 页仍未收敛（total 可能在读取期间持续增长）；"+
		"请收窄过滤条件，不要静默截断", base, query, maxPages)
}

// simListAll 读**已分页**端点的全部页并返回 items，失败即终止场景。
func simListAll[T any](e *harness.Env, base, query string) []T {
	e.T.Helper()
	items, err := simPageAll[T](e, base, query)
	if err != nil {
		e.Fatalf("%v", err)
	}
	return items
}

// simDecodePage 解析**已分页**端点的 data 段（只取当前页），失败即终止场景。
func simDecodePage[T any](e *harness.Env, r *harness.Response, path string) []T {
	e.T.Helper()
	items, err := simPageItems[T](r, path)
	if err != nil {
		e.Fatalf("%v", err)
	}
	return items
}

// simListGet 发一次 GET 并解析**已分页**端点信封的当前页，返回 items。
func simListGet[T any](e *harness.Env, path string) []T {
	e.T.Helper()
	return simDecodePage[T](e, e.Admin.Get(path), path)
}

// simBareListItems 解析**契约就是裸数组**的端点，返回 (rows, error)。
//
// 非致命版本，理由同 simPageItems。
func simBareListItems[T any](r *harness.Response, path string) ([]T, error) {
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("GET %s 返回 %d（期望 200）: %s", path, r.Status, r.BodyString())
	}
	var rows []T
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		return nil, fmt.Errorf("解析 GET %s 失败（该端点的契约是**裸数组**）: %w（data=%s）",
			path, err, simHead(string(r.Data), 200))
	}
	return rows, nil
}

// simBareListGet 发一次 GET 并解析**契约就是裸数组**的端点，返回行，失败即终止场景。
//
// 它与 simListGet（分页信封）并存不是冗余：把"该端点没有分页"这一产品事实
// **显式写在调用点**，下一个读者不必再去翻 handler 源码判断。反过来，
// 若端点被改成信封，simBareListItems 会因为「object into slice」立刻失败
// 并打印端点路径 —— 形状往任何方向漂移都会响。
//
// 命名用 sim 前缀（门禁 8）：它要被多个域调用，只能住在 catalog.go。
func simBareListGet[T any](e *harness.Env, path string) []T {
	e.T.Helper()
	rows, err := simBareListItems[T](e.Admin.Get(path), path)
	if err != nil {
		e.Fatalf("%v", err)
	}
	return rows
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
