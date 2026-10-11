package main

// device_op_http_e2e_test.go -- task-27：把"操作员点重启"的**完整链路**接起来测。
//
// # 为什么需要这个文件
//
// 这条链路此前只在**两端各自**被测过，中间那段真 TCP 路径从没合起来跑过：
//
//   - internal/api/handler_node_device_op_test.go 测了 HTTP 面（200/202/409/502/501），
//     但用的是 swallowPublisher —— 下行只是被"记下来"，**没有真的走 TCP 到设备**；
//   - cmd/server/device_e2e_test.go 测了传输层的 0x22/0x23 往返，但**不经过 HTTP**。
//
// 本文件把两者串起来，全程用**生产组件**：
//
//   httptest → gin(生产 SetupRoutes) → JWTAuthWithDB → registerNodeDeviceOpRoutes
//     → nodemgr.Manager.SendDeviceOp → downlink.Bridge → transport.Server(真 mTLS)
//     → 真 TLS 设备连接 → 0x23 ACK → DeviceOpTracker → HTTP 响应
//
// # 唯一不是生产的东西
//
// 设备端是本测试里的一个真 TLS 客户端（回 ACK 的字节由 pkg/frame 编码）。
// 真正的固件客户端另有跨语言对锚覆盖（tools/run_firmware_backend_e2e.sh）。
//
// # 三条覆盖，各自回答一个不同的问题
//
//   1. ACK 到达        → 200：设备做了。
//   2. 超时未 ACK      → 202：发出去了、结果未知。**这不是失败** ——
//                        一个 ACK 丢了的重启仍然重启了。报失败会白跑一趟现场；
//                        报成功是撒谎。
//   3. 断链            → 必须**不是 200**，且不得声称 acked。
//
// 第 3 条按"断在哪一步"分成两个子场景，因为正确答案本来就不一样：
//   3a 送不到任何地方（无 TCP 会话 + MQTT 也失败）⇒ 502（明确错误码）；
//   3b 送到了设备 socket、但设备没回就断了 ⇒ 202（诚实的"未知"）。
//   ⚠ 两者共同的红线是**不能变成 200**：那会把"不知道"说成"成功"。

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/api"
	authservice "ehome/backend/internal/auth"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/transport"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/protoframe"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ⚠ countingLegacy（MQTT 兜底计数器）已随 MQTT 一起删除（2026-10-08）。
//
// 它原本用来证明"下行确实走了 TCP 而不是回落 MQTT"。MQTT 已不存在 ⇒ 没有
// 回落通道可走，这条性质变成**结构性**的：downlink.Publish 在没有会话时
// 直接返回 error（见 internal/downlink），"没送到却回 200" 不再可能。
// 下面第 3 条用例仍然覆盖"断链 ⇒ 明确错误码"，只是不再需要计数器。
// 注：sync/errors 两个 import 随之删除（见文件头）。

// httpOpEnv 是一条**完整装配**的链路句柄。
type httpOpEnv struct {
	engine *gin.Engine
	srv    *transport.Server
	mgr    *nodemgr.Manager
	addr   string
	db     *gorm.DB
	pki    *e2ePKI
	token  string
	nodeID string
}

// e2eJWTSecret 复刻 internal/api 对 jwtSecret 的派生规则。
//
// ⚠ 为什么必须复刻：`api.jwtSecret` **未导出**，而 SetupRoutes 只在 v1 组上装
// JWTAuthWithDB —— 想用**生产装配入口**就必须能签出它认的令牌。
// 这是本文件唯一一处"照抄常量"，所以单独成函数并注明来源；
// 一旦派生规则变了，下面用例会以 401 **响亮地红**，而不是静默地测了个假东西。
func e2eJWTSecret() []byte {
	if s := os.Getenv("EHOME_JWT_SECRET"); s != "" {
		return []byte(s)
	}
	return []byte("ehome-dev-secret-change-me") // internal/api/middleware.go:17
}

// newHTTPOpEnv 起一条全真实链路，并造出"已初始化 + 管理员 + 有效会话"的最小鉴权状态。
//
// ⚠ 传输层与 HTTP 层必须共用**同一个** db / hub / manager：
// device_e2e_test.go 记过一次教训 —— registry 若被两半各建一个，
// 设备会认证成功、发 Hello、然后**什么都收不到**，而且没有报错。
func newHTTPOpEnv(t *testing.T, nodeID string) *httpOpEnv {
	t.Helper()
	pki := newE2EPKI(t, nodeID)
	srv, mgr, addr, db, hub := startE2EServerEx(t, pki)

	// 内存 SQLite 的每条连接是**独立的库**；链路里的 DB 访问跨 goroutine，
	// 钉住连接池才能保证它们看到同一份数据（既有用例同款做法）。
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}

	if err := db.AutoMigrate(&models.Node{}); err != nil {
		t.Fatalf("migrate nodes: %v", err)
	}
	// HTTP 处理器按 nodeID 查库（handler_node.go:23 findNodeByID）。
	if err := db.Create(&models.Node{NodeID: nodeID, Name: "http-op", Status: "online"}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	token := seedSessionForHTTPOp(t, db)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// 生产装配入口（main.go 用的就是它）。otaMgr/driverRegistry 传 nil 是安全的
	// —— 既有 SetupRoutes 用例已证明注册期不解引用它们。
	api.SetupRoutes(engine, db, hub, mgr, nil, nil)

	return &httpOpEnv{engine: engine, srv: srv, mgr: mgr, addr: addr,
		db: db, pki: pki, token: token, nodeID: nodeID}
}

func seedSessionForHTTPOp(t *testing.T, db *gorm.DB) string {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&models.AuthState{Key: models.SystemAuthStateKey,
		State: models.AuthStateInitialized, SecurityVersion: 1, InitializedAt: &now}).Error; err != nil {
		t.Fatalf("seed auth_state: %v", err)
	}
	subjectKey := models.SystemAdminSubjectKey
	user := models.User{Username: "http-op-admin", PasswordHash: "hash", Role: "admin",
		Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, InitializedAt: &now}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	token, err := authservice.SignSessionToken(user, e2eJWTSecret(), time.Hour)
	if err != nil {
		t.Fatalf("sign session token: %v", err)
	}
	return token
}

// postDeviceOp 打真实路由（带生产要求的 Bearer 会话）。
func postDeviceOp(t *testing.T, env *httpOpEnv, nodeID, op string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"op": op})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/device-ops",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.token)
	w := httptest.NewRecorder()
	env.engine.ServeHTTP(w, req)
	return w
}

// opResponse 是响应体里我们关心的字段（envelope: {code,data,message}）。
type opResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		RequestID string `json:"request_id"`
		Op        string `json:"op"`
		Acked     bool   `json:"acked"`
		Result    string `json:"result"`
	} `json:"data"`
}

func decodeOpResponse(t *testing.T, w *httptest.ResponseRecorder) opResponse {
	t.Helper()
	var out opResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应体不是 JSON: %v\n%s", err, w.Body.String())
	}
	return out
}

// dialDeviceForOp 拨一条真 mTLS 连接，并完成应用层握手（等 HelloAck）。
//
// HelloAck 是"会话已建立"的唯一信号；不等它就开始发 0x22，
// Bridge 会因为 HasSession 为假而回落到 MQTT（那样就测不到 TCP 了）。
func dialDeviceForOp(t *testing.T, env *httpOpEnv) *tls.Conn {
	t.Helper()
	conn := dialE2EDevice(t, env.pki, env.addr)
	for i := 0; i < 600 && !env.srv.Registry().HasSession(env.nodeID); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := conn.Write(buildHelloFrame(t, env.nodeID, 4242)); err != nil {
		t.Fatalf("write Hello: %v", err)
	}
	if got := readOneFrame(t, conn, 5*time.Second); got == nil {
		t.Fatal("没有 HelloAck：会话没建立，后续不可能走 TCP")
	}
	if !env.srv.Registry().HasSession(env.nodeID) {
		t.Fatal("收到 HelloAck 但 registry 仍无此会话 —— 传输层与 bridge 看到的不是同一个 registry")
	}
	return conn
}

// ackDeviceOp 读一条 0x22 下行并按固件的方式回 0x23。
//
// ⚠ 用 readUntilType（而不是"读下一帧并断言它是 0x22"）：
// 服务端在 Hello 之后会**主动下发** 0x04(config_manifest) 与 0x08(ping)，
// 谁先到由调度决定。device_e2e_test.go:274-314 已把这条教训写死在那里，
// 且它只放行**有出处**的交错类型 —— 其余类型立刻失败，不静默跳过。
// 本函数复用同一份允许列表，不另造一个宽松版本（那会把真缺陷一起吞掉）。
func ackDeviceOp(t *testing.T, conn *tls.Conn, wantOp frame.DeviceOp,
	result frame.DeviceOpResult) string {
	t.Helper()
	down := readUntilType(t, conn, frame.MsgDeviceOp, serverInitiatedInterleaves, 10*time.Second)
	if down == nil {
		t.Fatal("设备侧没有收到操作下行 —— 链路没接通")
	}
	h, err := protoframe.DecodeHeader(down)
	if err != nil {
		t.Fatalf("下行帧无法解码: %v", err)
	}
	if h.Type != frame.MsgDeviceOp {
		t.Fatalf("下行类型 = 0x%02X, 期望 0x%02X", h.Type, frame.MsgDeviceOp)
	}
	req, err := frame.DecodeDeviceOp(down[protoframe.HeaderSize:])
	if err != nil {
		t.Fatalf("下行 0x22 载荷无法解码: %v", err)
	}
	if req.Op != wantOp {
		t.Fatalf("线上 op = %d, 期望 %d（操作员点了重启却被做成别的）", req.Op, wantOp)
	}
	if req.RequestID == "" {
		t.Fatal("下行没有 request_id，ACK 无法与请求关联")
	}

	ackPayload, err := frame.EncodeDeviceOpAck(req.RequestID, result, "")
	if err != nil {
		t.Fatalf("编码 ACK: %v", err)
	}
	ackFrame := make([]byte, protoframe.HeaderSize+len(ackPayload))
	ah := protoframe.Header{Ver: protoframe.Version, Type: frame.MsgDeviceOpAck,
		PayloadLen: uint16(len(ackPayload))}
	if err := protoframe.EncodeHeader(ackFrame, ah); err != nil {
		t.Fatalf("编码 ACK 头: %v", err)
	}
	copy(ackFrame[protoframe.HeaderSize:], ackPayload)
	if _, err := conn.Write(ackFrame); err != nil {
		t.Fatalf("写 ACK: %v", err)
	}
	return req.RequestID
}

const httpOpNodeID = "http-op-node" // 非纯数字：findNodeByID 先试 Atoi 再按 node_id 查

// ══════════════════════════════════════════════════════════════════════════
// 1) ACK 到达 ⇒ 200 + acked=true + result 与设备回的一致
// ══════════════════════════════════════════════════════════════════════════

func TestHTTPDeviceOp_ACKReachesDevice_Returns200(t *testing.T) {
	env := newHTTPOpEnv(t, httpOpNodeID)
	env.mgr.SetDeviceOpTimeout(10 * time.Second)

	// 先证明这条路由**确实在生产鉴权之后**（不是被绕过）。
	//
	// 为什么值得先花这几行：本用例是拿一个**自己签的**令牌去打 SetupRoutes 的。
	// 万一 SetupRoutes 忘了给 v1 组装 JWTAuthWithDB，后面那条 200 依然会绿 ——
	// 而那时它证明的就不是"真实鉴权链路"了。无令牌必须 401，这才把前提钉住。
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+env.nodeID+"/device-ops",
			strings.NewReader("{\"op\":\"reboot\"}"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		env.engine.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("无令牌的 device-ops 请求返回 %d, 期望 401 —— 这条路由没走生产鉴权, "+
				"那本用例后面证明的就不是真实 HTTP 链路", w.Code)
		}
	}

	// 会话必须在 HTTP 请求之前建立：否则 Bridge 找不到会话，会回落到 MQTT。
	conn := dialDeviceForOp(t, env)

	// ⚠ HTTP 请求会**阻塞**到收到 ACK（这是设计：接口是同步的）。
	// 所以 POST 放 goroutine、设备侧读取留在测试主 goroutine ——
	// readUntilType 内部会调 t.Fatalf，而 t.Fatalf **不能**在非测试 goroutine 里调
	// （它只会退出那个 goroutine，测试照旧往下跑）。
	type result struct {
		w   *httptest.ResponseRecorder
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resCh <- result{nil, errors.New("panic in ServeHTTP")}
			}
		}()
		resCh <- result{postDeviceOp(t, env, env.nodeID, "reboot"), nil}
	}()

	requestID := ackDeviceOp(t, conn, frame.DeviceOpReboot, frame.DeviceOpOK)

	var got result
	select {
	case got = <-resCh:
	case <-time.After(15 * time.Second):
		t.Fatal("HTTP 请求没有返回（设备已 ACK，说明 tracker 没被唤醒）")
	}
	if got.err != nil {
		t.Fatalf("请求 goroutine 出错: %v", got.err)
	}
	w := got.w

	if w.Code != http.StatusOK {
		t.Fatalf("ACK 已到达，HTTP 应回 200，实际 %d：%s", w.Code, w.Body.String())
	}
	body := decodeOpResponse(t, w)
	if body.Data == nil {
		t.Fatalf("200 响应没有 data 对象：%s", w.Body.String())
	}
	if !body.Data.Acked {
		t.Fatalf("设备已回 ACK，但响应里 acked=false：%s", w.Body.String())
	}
	if body.Data.Result != frame.DeviceOpResultName(frame.DeviceOpOK) {
		t.Fatalf("result = %q, 期望 %q（设备回的结果码没有原样传到 HTTP）",
			body.Data.Result, frame.DeviceOpResultName(frame.DeviceOpOK))
	}
	if body.Data.RequestID != requestID {
		t.Fatalf("响应 request_id = %q, 设备 ACK 的是 %q —— 关联错了就说明配错了请求",
			body.Data.RequestID, requestID)
	}
}

// ══════════════════════════════════════════════════════════════════════════
// 2) 超时未 ACK ⇒ 202 + acked=false（**不是**失败）
// ══════════════════════════════════════════════════════════════════════════

// 这条是这套 API 最值得测的语义：200 与 202 的区别就是
// "设备做了" 与 "发出去了、不知道做没做"。
// 把它当成失败（502/500）会让操作员白跑一趟现场；当成成功（200）是撒谎。
func TestHTTPDeviceOp_NoACKReturns202NotError(t *testing.T) {
	env := newHTTPOpEnv(t, httpOpNodeID)
	// 短超时：这条用例要等的就是"没有 ACK"。
	env.mgr.SetDeviceOpTimeout(250 * time.Millisecond)

	conn := dialDeviceForOp(t, env)
	// ⚠ 设备**连上但不回 ACK** —— 这正是"设备崩了/卡住"的真实形态。
	_ = conn

	w := postDeviceOp(t, env, env.nodeID, "reboot")

	if w.Code != http.StatusAccepted {
		t.Fatalf("未收到 ACK 时应回 202（已发出、结果未知），实际 %d：%s",
			w.Code, w.Body.String())
	}
	body := decodeOpResponse(t, w)
	if body.Data == nil {
		t.Fatalf("202 响应没有 data 对象：%s", w.Body.String())
	}
	if body.Data.Acked {
		t.Fatal("设备根本没回 ACK，202 响应却声称 acked=true")
	}
}

// ══════════════════════════════════════════════════════════════════════════
// 3a) 断链且**哪都送不到** ⇒ 502（明确错误码）
// ══════════════════════════════════════════════════════════════════════════

// 场景：设备从未连上（或已断开），TCP 没有会话；MQTT 这条路也送不到。
// 正确答案是**明确告诉操作员"没送到"**（502），而不是"已受理"。
//
// ⚠ MQTT 移除后这条路径**变简单了**：没有"socket 断了但 broker 收下了"这种
// 中间态。没有会话就是没有投递，error 直接冒到 HTTP 层 ⇒ 502。
// （改动前它靠"让兜底 publisher 返回错误"来构造同一种语义。）
func TestHTTPDeviceOp_DisconnectedNoPathReturnsExplicitError(t *testing.T) {
	env := newHTTPOpEnv(t, httpOpNodeID)
	env.mgr.SetDeviceOpTimeout(500 * time.Millisecond)

	// ⚠ 刻意**不**建立设备会话：这就是断链。
	w := postDeviceOp(t, env, env.nodeID, "reboot")

	if w.Code == http.StatusOK {
		t.Fatalf("断链且哪都送不到，却回了 200 —— 把\"没送到\"说成了\"设备做了\"：%s",
			w.Body.String())
	}
	if w.Code != http.StatusBadGateway {
		t.Fatalf("下行没能交给任何传输时应回 502，实际 %d：%s", w.Code, w.Body.String())
	}
	// ⚠ 这里原本还有一条"兜底被调用过"的断言，用来证明覆盖了"没有 TCP 会话"
	// 的分支。MQTT 移除后没有兜底可查；该分支现在由**上面两条**断言覆盖：
	// 非 200 + 恰好 502（502 只在"无法交给任何传输"时产生）。
	// 502 的 body 是错误封装（data=null），绝不能含 acked=true。
	if strings.Contains(w.Body.String(), "\"acked\":true") {
		t.Fatalf("502 响应里出现了 acked=true：%s", w.Body.String())
	}
}

// ══════════════════════════════════════════════════════════════════════════
// 3b) 送到了设备、但设备**没回就断** ⇒ 仍必须是"未知"（202），绝不是 200
// ══════════════════════════════════════════════════════════════════════════

// 这是"操作进行中断开"的字面场景：0x22 已经写进了设备的 socket（设备读到了），
// 然后它断开、不发 ACK。此刻服务端的知识是"发出去了，不知道做没做"——
// 与场景 2 同义，所以也应当是 202；区别在于我们已经**证明**设备确实收到了。
//
// 红线与 3a 相同：**不能是 200**。
func TestHTTPDeviceOp_DeviceDropsMidOperationIsNotSuccess(t *testing.T) {
	env := newHTTPOpEnv(t, httpOpNodeID)
	env.mgr.SetDeviceOpTimeout(800 * time.Millisecond)

	conn := dialDeviceForOp(t, env)

	resCh := make(chan *httptest.ResponseRecorder, 1)
	go func() { resCh <- postDeviceOp(t, env, env.nodeID, "reboot") }()

	// 设备侧：读到下行（证明真的送到了），然后**不回 ACK**、直接断开。
	down := readUntilType(t, conn, frame.MsgDeviceOp, serverInitiatedInterleaves, 10*time.Second)
	if down == nil {
		t.Fatal("设备没收到下行 —— 那不是\"操作中断开\"，是链路根本没通")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("关闭设备连接: %v", err)
	}

	var w *httptest.ResponseRecorder
	select {
	case w = <-resCh:
	case <-time.After(15 * time.Second):
		t.Fatal("HTTP 请求没有返回")
	}

	if w.Code == http.StatusOK {
		t.Fatalf("设备断开且没有 ACK，却回了 200 —— 这是把\"不知道\"说成\"成功\"，"+
			"操作员会以为重启已完成：%s", w.Body.String())
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("设备收到了下行但没回就断开 ⇒ 应回 202（已投递、结果未知），实际 %d：%s",
			w.Code, w.Body.String())
	}
	body := decodeOpResponse(t, w)
	if body.Data == nil {
		t.Fatalf("202 响应没有 data 对象：%s", w.Body.String())
	}
	if body.Data.Acked {
		t.Fatal("设备断开且没回 ACK，响应却声称 acked=true")
	}
	// 下行确实走的是 TCP：上面 readUntilType 已经把 0x22 从设备 socket 里读出来了，
	// 那本身就是"送到了"的证据（MQTT 移除后不再需要计数器来排除误投）。
}
