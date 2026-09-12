//go:build simulation

package harness

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Options 是设计 §5.1 冻结的运行选项。
type Options struct {
	RunID     string // 缺省 = sim-<UTC 日期>-<4 位随机>
	DSN       string // 缺省从 EHOME_DB_* 环境变量组装
	MQTTAddr  string // 缺省 tcp://127.0.0.1:1883
	AdminUser string // 缺省 "admin"
	AdminPass string // 缺省随机强口令
	KeepDB    bool   // 缺省 false：运行结束 DROP 场景库
	LogLevel  string // 缺省 "info"
}

// DefaultOptions 返回设计文档规定的全部缺省值。
func DefaultOptions() Options { return Options{} }

// StartupRecord 记录启动序列中"只在启动时刻可观测"的事实。
//
// 为什么需要它：SIM-DEP-001/002/003 要证明的不变量（未初始化状态、
// 一次性凭据、错误凭据不可绕过）在 Env.Start 完成后已不可复现——系统
// 必然已初始化。harness 因此在启动序列里**原样记录**这些观测，
// 场景断言的是"当时真实发生的 HTTP 往返"，而不是重放或伪造。
type StartupRecord struct {
	CredentialLine string
	Credential     string // 仅存在于内存，不写入 summary.json

	StateBeforeInitialize     string
	WrongCredentialStatus     int
	WrongCredentialCode       string
	WrongCredentialMessage    string
	WrongCredentialBody       string
	StateAfterRejectedAttempt string

	InitializeStatus     int
	InitializeMessage    string
	InitializeBody       string
	StateAfterInitialize string
	AdminUserID          int64
	AdminUsername        string
}

// Env 是一次仿真运行的全部上下文，供所有场景复用。
type Env struct {
	T       *testing.T // 当前场景的 *testing.T（每个子测试开始时重绑定）
	RunID   string
	BaseURL string
	Admin   *Session
	DBName  string
	Logs    *LogBuffer

	Options   Options
	AdminUser string
	AdminPass string
	MQTTAddr  string
	Startup   StartupRecord

	owner   *testing.T // Start 的宿主 T，收尾阶段必须用它（子测试 T 已结束）
	runDir  string
	lock    *runLock // run 级排他锁，nil 表示未持有（见 acquireRunLock）
	workDir string   // 子进程 cwd（临时目录，见 startServer 注释）
	summary *Summary
	admin   *databaseAdmin
	binPath string

	serverCmd  *exec.Cmd
	serverDone chan struct{}
	serverExit error

	sqlOnce sync.Once
	sqlDB   *sql.DB
	sqlErr  error

	devMu   sync.Mutex
	devices []*Device

	evMu        sync.Mutex
	current     *ScenarioRecord
	lastFailure string
}

// credentialLinePattern 对应 backend/cmd/server/main.go:
//
//	logger.Infof("Initialization credential (valid for 10 minutes): %s", credential)
//
// 生产代码若改动行文，harness 必须同步；此处失败信息会直接指出该行。
var credentialLinePattern = regexp.MustCompile("Initialization credential \\(valid for 10 minutes\\): (\\S+)")

const (
	healthTimeout     = 60 * time.Second
	credentialTimeout = 30 * time.Second
	mqttReadyTimeout  = 30 * time.Second
	buildTimeout      = 10 * time.Minute
	shutdownGrace     = 12 * time.Second
)

// Preflight 在不建立任何持久状态的前提下检查基础设施是否可用：
// PG（维护库）可达、MQTT broker 可达、./cmd/server 可编译。
//
// 为什么需要它：设计 §5.5 要求"基础设施缺失时 TestMain 直接失败并给出
// 可执行提示，不静默跳过"。TestMain 拿不到 *testing.T，无法调用 Start
// （Start 的 t.Cleanup 负责杀子进程/删库，合成 T 的 Cleanup 永不执行），
// 因此由 Preflight 承担这一步，Start 内部自然也会重复走到同样的检查。
func Preflight(opts Options) error {
	resolved := opts.withDefaults(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	admin, err := newDatabaseAdmin(ctx, resolved.DSN)
	if err != nil {
		return err
	}
	if err := admin.Close(); err != nil {
		return fmt.Errorf("关闭维护库连接失败: %w", err)
	}

	address, err := brokerAddress(resolved.MQTTAddr)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return fmt.Errorf("MQTT broker 不可达（%s）: %v；请先执行 make infra", resolved.MQTTAddr, err)
	}
	_ = conn.Close()

	if _, err := serverBinary(); err != nil {
		return err
	}
	return nil
}

// brokerAddress 把 tcp://host:port 归一为 net.Dial 可用的 host:port。
func brokerAddress(broker string) (string, error) {
	trimmed := strings.TrimSpace(broker)
	for _, prefix := range []string{"tcp://", "mqtt://", "ssl://", "tls://"} {
		trimmed = strings.TrimPrefix(trimmed, prefix)
	}
	if trimmed == "" || !strings.Contains(trimmed, ":") {
		return "", fmt.Errorf("MQTT_BROKER %q 不是合法的 host:port 形式", broker)
	}
	return trimmed, nil
}

// Start 完成一次仿真运行的全部准备（设计 §5.1 启动序列 1~9 步）。
// 任一步失败都以可执行的错误信息终止测试，绝不静默降级。
//
// 第 0 步（本实现新增）是获取 run 级排他锁：这是**在所有步骤之前**的前置条件，
// 拿不到锁的 run 连库都不会碰（见 acquireRunLock / cleanupOrphanDbs 的注释）。
func Start(t *testing.T, opts Options) *Env {
	t.Helper()

	e := &Env{T: t, owner: t, Logs: NewLogBuffer()}
	e.Options = opts.withDefaults(t)
	e.RunID = e.Options.RunID
	e.AdminUser = e.Options.AdminUser
	e.AdminPass = e.Options.AdminPass
	e.MQTTAddr = e.Options.MQTTAddr
	e.DBName = DatabaseNameForRun(e.RunID)

	root := repoRoot(t)

	// 步骤 0：run 级排他锁。必须在**创建场景库之前** —— 抢不到锁就直接终止，
	// 绝不先建库再失败（那正是孤儿库堆积的来源）。
	e.acquireRunLock(t, root)
	// 释放幂等，且与 finish 里的释放重复注册是有意的：这里保证「Start 中途
	// Fatal」也能立刻让位，finish 里的那次保证「DROP 场景库之后」才解锁。
	t.Cleanup(e.releaseRunLock)

	// 步骤 1：库名红线校验。校验发生在建立任何连接之前。
	if err := ValidateDatabaseName(e.DBName); err != nil {
		t.Fatalf("场景库名不合规: %v", err)
	}

	e.runDir = filepath.Join(root, ".logs", "simulation", e.RunID)
	if err := os.MkdirAll(e.runDir, 0o755); err != nil {
		t.Fatalf("创建证据目录 %s 失败: %v", e.runDir, err)
	}
	e.summary = &Summary{
		RunID:     e.RunID,
		Database:  e.DBName,
		MQTTAddr:  e.MQTTAddr,
		StartedAt: time.Now().UTC(),
		Scenarios: []*ScenarioRecord{},
		path:      filepath.Join(e.runDir, "summary.json"),
	}

	// 步骤 9 提前注册：即使后续任一步 Fatal，收尾仍会杀子进程并 DROP 场景库。
	t.Cleanup(e.finish)

	ctx := context.Background()

	// 步骤 1~2：连维护库 → DROP（若存在）→ CREATE 场景库。
	admin, err := newDatabaseAdmin(ctx, e.Options.DSN)
	if err != nil {
		t.Fatalf("场景库准备失败: %v", err)
	}
	e.admin = admin

	// 步骤 1.5：清扫孤儿场景库。**必须在此处**——此刻已持有排他锁，
	// 能确认没有任何活跃 run 拥有这些库；不持锁就删会误伤正在运行的 run。
	cleanupOrphanDbs(t, admin, e.DBName)

	// 步骤 2：DROP（若存在）→ CREATE 当前 run 的场景库。
	if err := admin.EnsureDatabase(ctx, e.DBName); err != nil {
		t.Fatalf("准备场景库失败: %v", err)
	}

	// 步骤 3：取一个空闲端口。
	port := freePort(t)
	e.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	// 步骤 4：编译真实组合根（整个测试进程只编译一次）。
	binary, err := serverBinary()
	if err != nil {
		t.Fatalf("编译 ./cmd/server 失败（真实组合根不可用）: %v", err)
	}
	e.binPath = binary
	e.summary.ServerBin = binary

	// 步骤 5：以子进程启动服务，注入场景库与隔离参数。
	if err := e.startServer(port); err != nil {
		t.Fatalf("启动服务子进程失败: %v", err)
	}

	// 步骤 6：等待 /health。
	e.waitHealthy()

	// 步骤 7：从启动日志解析一次性初始化凭据。
	credential := e.waitCredential()

	// 步骤 7.5（本实现新增，见报告偏差清单）：确认服务端 MQTT 上行已就绪。
	// 否则首批 Hello 可能落在订阅建立之前被 broker 丢弃，造成假失败。
	e.waitMQTTReady()

	// 步骤 8：错误凭据探针 + 真实初始化 + 管理员登录。
	e.initialize(credential)

	e.note("run_id", e.RunID)
	e.note("database", e.DBName)
	e.note("base_url", e.BaseURL)
	e.note("mqtt_addr", e.MQTTAddr)
	e.note("admin_username", e.AdminUser)
	e.note("credential_selector", strings.SplitN(e.Startup.Credential, ".", 2)[0])
	e.note("state_before_initialize", e.Startup.StateBeforeInitialize)
	e.note("state_after_rejected_attempt", e.Startup.StateAfterRejectedAttempt)
	e.note("state_after_initialize", e.Startup.StateAfterInitialize)
	return e
}

// ---------- run 级排他锁 ----------

// acquireRunLock 获取 run 级排他锁（实现与理由见 lock.go）。
//
// 为什么在这里翻译错误信息：harness 的失败信息必须**可执行**。
// 超时不是"卡住"，而是"另一个 run 正在跑"，用户要立刻知道该等还是该查残留进程。
func (e *Env) acquireRunLock(t *testing.T, root string) {
	t.Helper()
	timeout := runLockWaitTimeout()
	t.Logf("获取 run 级排他锁 %s（等待上限 %s）...", filepath.Join(root, runLockPath), timeout)
	lock, err := acquireRunLock(root, timeout)
	if err != nil {
		t.Fatalf("%v", err)
	}
	e.lock = lock
	t.Logf("已获得 run 级排他锁（本 run 独占执行；锁文件 %s）", lock.path)
}

// releaseRunLock 释放 run 级排他锁。幂等：重复调用、未持有时调用都安全。
//
// 为什么在 finish 里再释放一次（Start 已经注册过一次）：锁必须活到
// **场景库 DROP 之后**。提前解锁会让下一个 run 在本 run 还在收尾时启动，
// 两个 run 又同时挂在共享 EMQX 上，排他性形同虚设。
func (e *Env) releaseRunLock() {
	if e.lock == nil {
		return
	}
	e.lock.release()
	e.lock = nil
}

// ---------- 启动子步骤 ----------

func (e *Env) startServer(port int) error {
	if e.admin == nil {
		return fmt.Errorf("内部错误：数据库管理句柄未初始化")
	}
	cfg := e.admin.config
	// CONFIG_PATH 指向运行目录下一个不存在的文件：使子进程只使用
	// 代码内默认值 + 本函数注入的环境变量，杜绝仓库里的 config.yaml
	// （它指向开发库 localhost:5434/ehome）影响仿真确定性。
	childEnv := mergeEnv(os.Environ(), map[string]string{
		"EHOME_DB_HOST":        cfg.Host,
		"EHOME_DB_PORT":        fmt.Sprintf("%d", cfg.Port),
		"EHOME_DB_USER":        cfg.User,
		"EHOME_DB_PASSWORD":    cfg.Password,
		"EHOME_DB_NAME":        e.DBName,
		"EHOME_SERVER_ADDR":    fmt.Sprintf(":%d", port),
		"MQTT_BROKER":          e.MQTTAddr,
		"EHOME_MQTT_CLIENT_ID": "ehome-sim-" + e.RunID,
		"EHOME_JWT_SECRET":     randHex(32),
		"EHOME_ENV":            "development",
		// GIN_MODE=debug 是必须的：middleware.go 的 isDevelopmentMode() 是
		// EHOME_ENV=development **且** GIN_MODE=debug 的与运算（该函数注释
		// 自称"GIN_MODE unset 也算 dev"，与实现不符，属产品侧注释缺陷）。
		// 缺了它会走生产分支，例如 handler_ota.go 在 EHOME_EXTERNAL_HOST
		// 为空时让固件上传直接 500。
		"GIN_MODE": "debug",
		// 固件下载 URL 的 host 与 Env.BaseURL 保持一致，让后续的
		// 带票据下载场景能直接对该 URL 发真实请求。
		"EHOME_EXTERNAL_HOST": fmt.Sprintf("127.0.0.1:%d", port),
		"LOG_LEVEL":           e.Options.LogLevel,
		"CONFIG_PATH":         filepath.Join(e.runDir, "no-config.yaml"),
		"SEED_TEST_DATA":      "",
		"EHOME_STATIC_DIR":    "",
		// 空字符串 = 不启用 CORS 中间件（与生产同源部署一致）。
		"EHOME_ALLOWED_ORIGINS": "",
	})

	// 子进程 cwd 指向临时目录：固件上传会写 <cwd>/firmwares/，
	// 绝不能污染仓库工作区。临时目录随收尾一并删除。
	workDir, err := os.MkdirTemp("", "ehome-sim-run-")
	if err != nil {
		return fmt.Errorf("创建子进程工作目录失败: %w", err)
	}
	e.workDir = workDir

	cmd := exec.Command(e.binPath)
	cmd.Dir = workDir
	cmd.Env = childEnv
	cmd.Stdout = e.Logs
	cmd.Stderr = e.Logs
	// 独立进程组：收尾时可整组终止，绝不留下孤儿服务进程。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec %s 失败: %w", e.binPath, err)
	}
	e.serverCmd = cmd
	e.serverDone = make(chan struct{})
	go func() {
		e.serverExit = cmd.Wait()
		close(e.serverDone)
	}()
	return nil
}

func (e *Env) stopServer() {
	if e.serverCmd == nil || e.serverCmd.Process == nil || e.serverDone == nil {
		return
	}
	select {
	case <-e.serverDone:
		return
	default:
	}
	// 先 SIGTERM（main.go 注册了优雅停机），超时再 SIGKILL。
	_ = e.serverCmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-e.serverDone:
		return
	case <-time.After(shutdownGrace):
	}
	_ = e.serverCmd.Process.Kill()
	select {
	case <-e.serverDone:
	case <-time.After(5 * time.Second):
	}
}

func (e *Env) waitHealthy() {
	e.T.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	err := e.EventuallyEveryError(healthTimeout, 500*time.Millisecond, func() error {
		select {
		case <-e.serverDone:
			return fmt.Errorf("服务子进程已退出（exit=%v）", e.serverExit)
		default:
		}
		resp, err := client.Get(e.BaseURL + "/health")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET /health status=%d", resp.StatusCode)
		}
		return nil
	})
	if err != nil {
		e.Fatalf("等待 %s 就绪失败: %v\n服务子进程日志尾部:\n%s", e.BaseURL+"/health", err, e.Logs.Tail(60))
	}
}

func (e *Env) waitCredential() string {
	e.T.Helper()
	matches, err := e.Logs.WaitForMatch(credentialLinePattern, credentialTimeout)
	if err != nil {
		e.Fatalf("解析一次性初始化凭据失败。该行由 backend/cmd/server/main.go 的 "+
			"logger.Infof(\"Initialization credential (valid for 10 minutes): %%s\", credential) 产出；"+
			"若生产代码改动了行文，harness 的正则必须同步。\n%v", err)
	}
	e.Startup.CredentialLine = matches[0]
	e.Startup.Credential = matches[1]
	return matches[1]
}

// waitMQTTReady 轮询直到服务端确实开始消费 MQTT 上行。
// 判据：故意发一个协议版本非法的 Hello，服务端会打 Warn
// "Rejecting invalid Hello before ACK"；该拒绝发生在任何入库之前，
// 因此这个探针不产生任何节点记录。
func (e *Env) waitMQTTReady() {
	e.T.Helper()
	probe := e.Device("warmup")
	if err := probe.Connect(); err != nil {
		e.Fatalf("仿真器连接 MQTT（%s）失败: %v；请先执行 make infra", e.MQTTAddr, err)
	}
	defer probe.Close()
	err := e.EventuallyEveryError(mqttReadyTimeout, 500*time.Millisecond, func() error {
		if _, err := probe.sendHello("0.0", "warmup", "warmup", 0, 0, false); err != nil {
			return err
		}
		if e.Logs.Contains("Rejecting invalid Hello") {
			return nil
		}
		return fmt.Errorf("服务端尚未消费 MQTT 上行")
	})
	if err != nil {
		e.Fatalf("等待服务端 MQTT 就绪失败: %v\n服务子进程日志尾部:\n%s", err, e.Logs.Tail(40))
	}
}

// initialize 执行设计 §5.1 第 8 步，并在前后各做一次可留证的观测。
func (e *Env) initialize(credential string) {
	e.T.Helper()
	anonymous := e.NewSession()

	before := anonymous.Get("/api/v1/auth/initialization")
	if before.Status != http.StatusOK {
		e.Fatalf("GET /api/v1/auth/initialization 失败: %s", before.context())
	}
	e.Startup.StateBeforeInitialize = before.DataString("state")
	if e.Startup.StateBeforeInitialize != "uninitialized" {
		e.Fatalf("全新部署的认证状态应为 uninitialized，实际 %q", e.Startup.StateBeforeInitialize)
	}

	// 错误凭据探针（SIM-DEP-003 的证据来源）：此刻系统仍未初始化，
	// 是唯一能观测"错误凭据被拒 + 状态不变"的窗口。
	wrong := anonymous.Post("/api/v1/auth/initialize", map[string]any{
		"credential": "sim-invalid-selector." + randHex(8),
		"username":   e.AdminUser,
		"password":   e.AdminPass,
		"email":      e.AdminUser + "@sim.invalid",
	})
	e.Startup.WrongCredentialStatus = wrong.Status
	e.Startup.WrongCredentialCode = wrong.ErrorCode
	e.Startup.WrongCredentialMessage = wrong.Message
	e.Startup.WrongCredentialBody = wrong.BodyString()
	if wrong.Status != http.StatusConflict {
		e.Fatalf("错误凭据初始化应被拒绝（409），实际: %s", wrong.context())
	}

	afterWrong := anonymous.Get("/api/v1/auth/initialization")
	e.Startup.StateAfterRejectedAttempt = afterWrong.DataString("state")
	if e.Startup.StateAfterRejectedAttempt != "uninitialized" {
		e.Fatalf("错误凭据尝试后系统仍应为 uninitialized，实际 %q", e.Startup.StateAfterRejectedAttempt)
	}

	created := anonymous.Post("/api/v1/auth/initialize", map[string]any{
		"credential": credential,
		"username":   e.AdminUser,
		"password":   e.AdminPass,
		"email":      e.AdminUser + "@sim.local",
	})
	e.Startup.InitializeStatus = created.Status
	e.Startup.InitializeMessage = created.Message
	e.Startup.InitializeBody = created.BodyString()
	if created.Status != http.StatusCreated {
		e.Fatalf("使用启动凭据初始化管理员失败: %s", created.context())
	}
	e.Startup.AdminUserID = created.DataInt("id")
	e.Startup.AdminUsername = created.DataString("username")

	after := anonymous.Get("/api/v1/auth/initialization")
	e.Startup.StateAfterInitialize = after.DataString("state")
	if e.Startup.StateAfterInitialize != "initialized" {
		e.Fatalf("初始化完成后认证状态应为 initialized，实际 %q", e.Startup.StateAfterInitialize)
	}

	admin, err := e.Login(e.AdminUser, e.AdminPass)
	if err != nil {
		e.Fatalf("管理员登录失败: %v", err)
	}
	e.Admin = admin
}

// RefreshAdmin 用当前管理员口令重建 Env.Admin 会话。
// 登出/改密场景会全局吊销会话（users.session_version++），
// 之后必须经由它恢复，后续场景才有可用令牌。
func (e *Env) RefreshAdmin() error {
	admin, err := e.Login(e.AdminUser, e.AdminPass)
	if err != nil {
		return err
	}
	e.Admin = admin
	return nil
}

// RefreshAdminWith 用指定口令重建 Env.Admin 会话，并把 Env.AdminPass
// 同步为新口令 —— 改密场景必须成对更新，否则后续场景会拿旧口令登录。
func (e *Env) RefreshAdminWith(password string) error {
	admin, err := e.Login(e.AdminUser, password)
	if err != nil {
		return err
	}
	e.Admin = admin
	e.AdminPass = password
	return nil
}

// ---------- 收尾 ----------

func (e *Env) finish() {
	// 收尾必须使用宿主 T：此刻最后一个子测试可能已经结束，
	// 在它上面调用 Errorf/Logf 会 panic（"Log in goroutine after test has completed"）。
	e.T = e.owner

	e.closeDevices()
	e.stopServer()

	logPath := ""
	if e.runDir != "" {
		logPath = filepath.Join(e.runDir, "server.log")
		if err := os.WriteFile(logPath, []byte(e.Logs.Full()), 0o644); err != nil {
			e.T.Errorf("写入服务日志 %s 失败: %v", logPath, err)
		}
	}

	if e.sqlDB != nil {
		_ = e.sqlDB.Close()
		e.sqlDB = nil
	}

	if e.workDir != "" {
		if err := os.RemoveAll(e.workDir); err != nil {
			e.T.Logf("清理子进程工作目录 %s 失败: %v", e.workDir, err)
		}
		e.workDir = ""
	}

	if e.admin != nil {
		if !e.Options.KeepDB {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := e.admin.DropDatabase(ctx, e.DBName); err != nil {
				e.T.Errorf("收尾 DROP 场景库 %s 失败: %v", e.DBName, err)
			}
			cancel()
		}
		_ = e.admin.Close()
	}

	// 场景库已经 DROP、子进程已经停：此刻才让出排他锁。
	// 释放幂等，重复调用（Start 也注册过一次）安全。
	e.releaseRunLock()

	if e.summary != nil {
		e.summary.FinishedAt = time.Now().UTC()
		e.summary.Status = "passed"
		for _, record := range e.summary.Scenarios {
			if record.Status != "passed" {
				e.summary.Status = "failed"
				break
			}
		}
		if err := e.summary.write(); err != nil {
			e.T.Errorf("写入 summary.json 失败: %v", err)
		} else {
			e.T.Logf("仿真证据: %s", e.summary.path)
		}
	}

	if e.T.Failed() {
		e.T.Logf("服务子进程日志尾部（完整日志 %s）:\n%s", logPath, e.Logs.Tail(80))
	}
}

// ---------- 场景记录与证据 ----------

// BeginScenario 开始记录一个场景（summary.json 的一条）。
func (e *Env) BeginScenario(id, title, domain string) *ScenarioRecord {
	record := &ScenarioRecord{ID: id, Title: title, Domain: domain, Status: "running", startedAt: time.Now()}
	e.evMu.Lock()
	e.current = record
	e.lastFailure = ""
	e.evMu.Unlock()
	if e.summary != nil {
		e.summary.record(record)
	}
	return record
}

// EndScenario 结束记录：失败原因取自最后一条 Fatalf 消息。
func (e *Env) EndScenario(record *ScenarioRecord, failed bool) {
	record.DurationMS = time.Since(record.startedAt).Milliseconds()
	e.evMu.Lock()
	if failed {
		record.Status = "failed"
		record.Failure = e.lastFailure
		if record.Failure == "" {
			record.Failure = "场景失败（无 harness 记录的失败消息，可能是场景自身断言或 panic）"
		}
	} else {
		record.Status = "passed"
	}
	e.current = nil
	e.evMu.Unlock()
	if e.summary != nil {
		e.summary.record(record)
	}
}

// Evidence 记录一条场景证据（设计 §5.1 / §8）。
// 关键断言旁必须写 Evidence：失败时它是唯一能还原现场的输入。
func (e *Env) Evidence(name string, value any) {
	e.evMu.Lock()
	defer e.evMu.Unlock()
	item := KV{Name: name, Value: normalizeEvidence(value)}
	if e.current != nil {
		e.current.Evidence = append(e.current.Evidence, item)
		return
	}
	if e.summary != nil {
		e.summary.Notes = append(e.summary.Notes, item)
	}
}

func (e *Env) note(name string, value any) { e.Evidence(name, value) }

// Fatalf 是 harness 内唯一的失败出口：记录失败消息后终止当前（子）测试。
func (e *Env) Fatalf(format string, args ...any) {
	e.T.Helper()
	message := fmt.Sprintf(format, args...)
	e.evMu.Lock()
	e.lastFailure = message
	e.evMu.Unlock()
	e.T.Fatalf("%s", message)
}

// ---------- 便捷访问器 ----------

// NS 生成带场景命名空间的实体名（设计 §5.6）：sim-<域>-<NNN>[-<suffix>]。
// 库本身已按运行隔离，命名空间只需避免跨场景撞名。
//
// 注意：返回值**不是**节点 ID。节点 ID 必须是 RunID + "-" + <局部名>
// （见 Env.Device），因为 nodes.node_id 是 varchar(32)，而 RunID 自身
// 已占用 17 个字符，容不下第二层 sim- 前缀。
func (e *Env) NS(scenarioID, suffix string) string {
	return "sim-" + sceneSlug(scenarioID, suffix)
}

// sceneSlug 把场景 ID 折叠为短横线小写 slug："SIM-NODE-003" + "n0" → "node-003-n0"。
// 供 NS 与节点局部名共用。
func sceneSlug(scenarioID, suffix string) string {
	base := strings.ToLower(strings.TrimPrefix(strings.ToUpper(scenarioID), "SIM-"))
	if suffix == "" {
		return base
	}
	return base + "-" + suffix
}

// Device 返回一个节点仿真器。local 是局部名，完整节点 ID 为
// RunID + "-" + local —— 即自动带上本运行的命名空间前缀（设计 §5.1），
// 且因为 RunID 固定以 "sim-" 开头而满足安全红线（设计 §7-2）。
//
// ⚠ local 必须在**整个运行范围内**唯一（它不含场景标识）。只按场景内
// 唯一来取名会在跨场景时撞名并直接 409 —— 实测 SIM-ALERT-001 与
// SIM-AUTO-001 都用了 "rule"。需要按场景隔离时请用 DeviceFor。
//
// local 必须足够短：nodes.node_id 是 varchar(32)，RunID 已占 17 字符。
func (e *Env) Device(local string) *Device {
	full := e.RunID + "-" + local
	if !strings.HasPrefix(full, "sim-") {
		e.Fatalf("节点 ID %q 必须以 sim- 开头（安全红线，设计 §7-2）", full)
	}
	if len(full) > maxNodeIDLength {
		e.Fatalf("节点 ID %q 长度 %d 超过 nodes.node_id varchar(%d)；请把局部名 %q 缩短到 %d 字符以内",
			full, len(full), maxNodeIDLength, local, maxNodeIDLength-len(e.RunID)-1)
	}
	return e.registerDevice(newDevice(e, full))
}

// maxNodeIDLength 与 models.Node.NodeID 的 gorm tag（varchar(32)）保持一致。
const maxNodeIDLength = 32

// sceneCodeByDomain 把场景域映射为紧凑场景码（设计 §5.6 v1.3 冻结）。
//
// 为什么需要紧凑码：nodes.node_id 是 varchar(32)，而 RunID（sim-YYYYMMDD-XXXX）
// 已占 17 个字符，局部名只剩 14 个字符。直接用场景 slug（"auto-006-manual"
// 就有 15 字符）会超长；2 字母域码 + 3 位序号（"at006-manual"，12 字符）
// 既保证唯一，又在预算内。
var sceneCodeByDomain = map[string]string{
	// 框架 §6 表的 13 个域。
	"DEP":   "dp",
	"AUTH":  "au",
	"NODE":  "nd",
	"CHAN":  "cn",
	"EDGE":  "eg",
	"DATA":  "dt",
	"DS":    "ds",
	"AUTO":  "at",
	"ALERT": "al",
	"CMD":   "cm",
	"OTA":   "ot",
	"RT":    "rt",
	"ERR":   "er",
	// 自动化引擎的 11 个子域（docs/设计/自动化引擎场景仿真验证.md §6 冻结）。
	// 缺少任何一项，DeviceFor 都会回退成 slug（例如 "scne-001-..."）并在
	// VARCHAR(32) 预算内被拒绝 —— 这正是该码表必须与设计文档同步的原因。
	"TRIG": "tg",
	"COND": "cd",
	"WIND": "wd",
	"DBLN": "db",
	"CNFM": "cf",
	"ACTN": "an",
	"MANU": "mn",
	"CRUD": "cu",
	"SCNE": "sn",
	"AUDT": "ad",
	"NTFY": "nf",
}

// scenarioRefPattern 解析 "SIM-<DOMAIN>-<NNN>"。
var scenarioRefPattern = regexp.MustCompile("^SIM-([A-Z]+)-([0-9]{3})$")

// sceneCode 由场景 ID 算出紧凑场景码："SIM-AUTO-006" → "at006"。
//
// 场景 ID 无法解析（形态不符或域码未知）时回退为整个小写去 SIM- 的 slug
// （"sim-custom-thing" → "custom-thing"），并返回 ok=false 让调用方在
// 错误信息里说明回退事实 —— 静默回退会让撞名问题重新变得不可见。
func sceneCode(scenarioID string) (code string, ok bool) {
	matches := scenarioRefPattern.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(scenarioID)))
	if matches != nil {
		if abbreviation, known := sceneCodeByDomain[matches[1]]; known {
			return abbreviation + matches[2], true
		}
	}
	slug := sceneSlug(scenarioID, "")
	if slug == "" {
		slug = "scene"
	}
	return slug, false
}

// ScenarioCodeFor 暴露场景码映射，供场景在拼自定义名字（通道名、设备名等）
// 时复用同一套紧凑前缀，避免各处自行发明。
func ScenarioCodeFor(scenarioID string) string {
	code, _ := sceneCode(scenarioID)
	return code
}

// DeviceFor 返回带「运行 + 场景」双重命名空间的节点仿真器（设计 §5.6 v1.3）。
//
// local 只需在**本场景内**唯一；跨场景的唯一性由紧凑场景码保证：
//
//	DeviceFor("SIM-AUTO-006", "manual") → sim-20260912-9485-at006-manual
//
// 这条 API 的存在原因是一次真实撞名事故：e.Device(local) 只拼 RunID、不含
// 场景标识，隔离性于是退化成"各域作者恰好选中不同后缀"这一脆弱假设 ——
// SIM-ALERT-001 与 SIM-AUTO-001 都用了 "rule"，实测直接 409
// （node_id already exists）。需要按场景隔离时请一律使用 DeviceFor。
//
// local 过长时立即失败并指明"把后缀缩短到 N 字符"，**绝不静默截断**
// —— 截断会重新引入撞名。
func (e *Env) DeviceFor(scenarioID, suffix string) *Device {
	code, exact := sceneCode(scenarioID)
	local := code
	if suffix != "" {
		local = code + "-" + suffix
	}
	full := e.RunID + "-" + local
	if !strings.HasPrefix(full, "sim-") {
		e.Fatalf("节点 ID %q 必须以 sim- 开头（安全红线，设计 §7-2）", full)
	}
	if len(full) > maxNodeIDLength {
		budget := maxNodeIDLength - len(e.RunID) - 1 - len(code) - 1
		if budget < 1 {
			e.Fatalf("无法在 nodes.node_id varchar(%d) 内容纳 RunID %q 与场景码 %q；"+
				"请为该场景使用更短的 RunID（Options.RunID）", maxNodeIDLength, e.RunID, code)
		}
		hint := ""
		if !exact {
			hint = fmt.Sprintf("；场景 ID %q 未能解析为紧凑场景码（期望形态 SIM-<DOMAIN>-<NNN>），"+
				"已回退为 slug %q，请改用合法场景 ID", scenarioID, code)
		}
		e.Fatalf("节点 ID %q 长度 %d 超过 nodes.node_id varchar(%d)：本次场景码为 %q，"+
			"请把后缀 %q 缩短到 %d 字符以内（不得截断——截断会导致跨场景撞名）%s",
			full, len(full), maxNodeIDLength, code, suffix, budget, hint)
	}
	return e.registerDevice(newDevice(e, full))
}

// registerDevice 把仿真器登记进 Env，由 Env 收尾统一断开连接。
func (e *Env) registerDevice(device *Device) *Device {
	e.devMu.Lock()
	e.devices = append(e.devices, device)
	e.devMu.Unlock()
	return device
}

func (e *Env) closeDevices() {
	e.devMu.Lock()
	devices := append([]*Device(nil), e.devices...)
	e.devices = nil
	e.devMu.Unlock()
	for _, device := range devices {
		device.Close()
	}
}

// SQL 返回场景库的直连句柄（只读断言与轮询收敛条件，设计 §3 原则 2）。
func (e *Env) SQL() *sql.DB {
	e.sqlOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		e.sqlDB, e.sqlErr = e.admin.OpenScenarioDatabase(ctx)
	})
	if e.sqlErr != nil {
		e.Fatalf("打开场景库连接失败: %v", e.sqlErr)
	}
	return e.sqlDB
}

// RunDir 返回本次运行的证据目录（.logs/simulation/<runid>）。
func (e *Env) RunDir() string { return e.runDir }

func (e *Env) commandContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// ---------- 工具函数 ----------

func (o Options) withDefaults(t *testing.T) Options {
	if t != nil {
		t.Helper()
	}
	if o.RunID == "" {
		o.RunID = "sim-" + time.Now().UTC().Format("20060102") + "-" + randHex(2)
	}
	if o.MQTTAddr == "" {
		o.MQTTAddr = envOr("MQTT_BROKER", "tcp://127.0.0.1:1883")
	}
	if o.AdminUser == "" {
		o.AdminUser = "admin"
	}
	if o.AdminPass == "" {
		// 随机强口令：仿真绝不使用任何真实/默认口令。
		o.AdminPass = "Sim-" + randHex(9) + "!aZ9"
	}
	if o.LogLevel == "" {
		o.LogLevel = "info"
	}
	if o.DSN == "" {
		o.DSN = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			envOr("EHOME_DB_HOST", "127.0.0.1"),
			envOr("EHOME_DB_PORT", "5432"),
			envOr("EHOME_DB_USER", "ehome"),
			envOr("EHOME_DB_PASSWORD", "ehome123"),
			DatabaseNameForRun(o.RunID),
		)
	}
	return o
}

// serverBinary 编译真实组合根，整个测试进程只编译一次（设计 §5.1 第 4 步）。
var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

func serverBinary() (string, error) {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ehome-sim-build-")
		if err != nil {
			buildErr = fmt.Errorf("创建构建目录失败: %w", err)
			return
		}
		out := filepath.Join(dir, "ehome-sim-server")
		ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, goTool(), "build", "-buildvcs=false", "-o", out, "./cmd/server")
		cmd.Dir = backendDirForBuild()
		var output strings.Builder
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err := cmd.Run(); err != nil {
			buildErr = fmt.Errorf("go build -buildvcs=false -o %s ./cmd/server: %w\n%s", out, err, output.String())
			return
		}
		buildPath = out
	})
	return buildPath, buildErr
}

// goTool 定位 go 可执行文件：优先 PATH，回退到当前测试二进制的 GOROOT。
func goTool() string {
	if path, err := exec.LookPath("go"); err == nil {
		return path
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

// repoRoot 从当前工作目录（go test 下为 backend/simulation）向上定位仓库根。
func repoRoot(t *testing.T) string {
	if value := os.Getenv("EHOME_REPO_ROOT"); value != "" {
		return value
	}
	dir, err := os.Getwd()
	if err != nil {
		if t != nil {
			t.Fatalf("获取工作目录失败: %v", err)
		}
		return "."
	}
	current := dir
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(current, "backend", "go.mod")); statErr == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return filepath.Clean(filepath.Join(dir, "..", ".."))
}

// backendDirForBuild 返回 backend 目录，供 go build 作为工作目录。
func backendDirForBuild() string { return filepath.Join(repoRoot(nil), "backend") }

// freePort 通过 net.Listen(":0") 取一个空闲端口后立即释放（设计 §5.1 第 3 步）。
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("释放探测端口失败: %v", err)
	}
	return port
}

// mergeEnv 覆盖环境变量：先剔除同名键再加入新值。
// 直接 append 会产生重复键，而 Go 的 getenv 只取第一个匹配——
// 那样注入的 EHOME_DB_NAME 会被外层 shell 里已有的同名变量顶掉。
func mergeEnv(base []string, overrides map[string]string) []string {
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replaced := overrides[name]; replaced {
				continue
			}
		}
		merged = append(merged, entry)
	}
	for name, value := range overrides {
		merged = append(merged, name+"="+value)
	}
	return merged
}

func randHex(bytesCount int) string {
	buffer := make([]byte, bytesCount)
	if _, err := rand.Read(buffer); err != nil {
		panic("harness: 生成随机数失败: " + err.Error())
	}
	return hex.EncodeToString(buffer)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
