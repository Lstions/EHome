//go:build simulation

package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runLockPath 是 run 级排他锁的锁文件路径（相对仓库根）。
//
// 为什么是 <repo>/.logs/simulation/.run.lock 而不是 /tmp：锁的作用域是
// "这一个仓库 + 这一套共享基础设施（PG + EMQX）"。放仓库内保证同一 checkout
// 的多个 run 争同一把锁，且与证据目录 .logs/simulation/ 同处一地便于诊断
// （README §1「一次运行的产物」）。
const runLockPath = ".logs/simulation/.run.lock"

// runLockPollInterval 是抢锁失败后的重试间隔。
const runLockPollInterval = time.Second

// runLockTimeout 是等待另一 run 结束的上限。
//
// 为什么给到 45 分钟：全量场景套件本身就可能跑很久（Makefile 的
// `go test -timeout=30m` 已经说明量级）。等待方必须比持有方更有耐心，
// 否则排在后面的 run 会在前一个 run 正常收尾之前就报错退出。
var runLockTimeout = 45 * time.Minute

// ---------- 锁的实现 ----------

// runLock 是一次 run 持有的排他锁。
//
// 为什么用 syscall.Flock 而不是 PID 文件：
//
//	PID 文件在持有者**崩溃**（panic / SIGKILL / 宿主掉电）时会留下残留，
//	下一个 run 看到"有主"就直接失败，或者被迫猜"这个 PID 还活着吗"——
//	而 PID 会被复用，猜错就是永久死锁。flock 的锁与打开的文件描述符绑定，
//	由**内核**在进程退出（含崩溃）时自动释放，因此最坏情况只是浪费一次
//	等待，绝不会留下需要人工清理的锁。
//
//	锁文件里仍然写入 PID 与开始时间，但那只是**诊断信息**（回答"谁在持有、
//	持有多久"），不是锁本身：判定所有权永远只看 flock 的返回值。
type runLock struct {
	file *os.File
	path string
}

// lockWaitTimeoutEnv 覆盖等待上限的环境变量（Go duration 字面量，如 "5s"）。
//
// 为什么需要它：等待上限默认 45 分钟（让排在后面的 run 有耐心等全量套件跑完），
// 这让"排他性验证"变得不可操作 —— 想观测超时错误就得等 45 分钟。
//
// 为什么它不会削弱互斥：这个值只影响**抢不到锁时愿意等多久**，绝不参与
// "谁能拿到锁"的判定 —— 判定永远只看 flock 的返回值，且锁被持有时无论
// timeout 多小都拿不到。把上限调小只会让等待方更早、更响亮地失败。
const lockWaitTimeoutEnv = "EHOME_SIM_LOCK_WAIT"

// runLockWaitTimeout 解析实际使用的等待上限。
func runLockWaitTimeout() time.Duration {
	raw := strings.TrimSpace(envOr(lockWaitTimeoutEnv, ""))
	if raw == "" {
		return runLockTimeout
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		// 解析失败绝不静默退回默认值：那会让"我明明设了 5s 却等了 45 分钟"
		// 变成一次要重新排查的事故。直接以可读信息失败。
		panic(fmt.Sprintf("环境变量 %s=%q 不是合法的正 duration（例如 5s / 1m）: %v",
			lockWaitTimeoutEnv, raw, err))
	}
	return parsed
}

// acquireRunLock 以非阻塞 flock 尝试获取 run 级排他锁；
// 拿不到就每秒重试，直到超时（默认 45 分钟）。
//
// 超时后返回的错误面向**可执行**：谁在持有（PID + 已持有时间）、
// 为什么不能并发（共享 EMQX 的无作用域通配订阅）、下一步做什么（等待或查残留进程）。
func acquireRunLock(root string, timeout time.Duration) (*runLock, error) {
	path := filepath.Join(root, runLockPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建锁目录 %s 失败: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件 %s 失败: %w", path, err)
	}

	deadline := time.Now().Add(timeout)
	for {
		// LOCK_EX|LOCK_NB：非阻塞尝试。成功即持有，失败立即返回 EWOULDBLOCK。
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			// 例如锁目录所在的文件系统不支持 flock：明确失败，绝不"降级为无锁"。
			// 静默降级会让多个 run 再次并发布满共享 EMQX，且失败完全不可见。
			_ = file.Close()
			return nil, fmt.Errorf("对锁文件 %s 加 flock 失败: %w"+
				"（若该路径在不支持 flock 的文件系统上，请把仓库放到本地磁盘）", path, err)
		}
		if time.Now().After(deadline) {
			holder := describeLockHolder(path)
			_ = file.Close()
			return nil, fmt.Errorf("另一个场景仿真 run 正在执行（%s）；已等待 %s\n"+
				"多个 run 并发会通过共享 EMQX 互相污染（nodes/+/up 是无作用域通配订阅）："+
				"A run 的服务端会收到 B run 的节点上行，并按数字 edge_device_id 落进 A run 的场景库，"+
				"触发 A run 的告警与自动化规则 —— 套件结果不可信。\n"+
				"请等待其结束，或先确认无残留进程：pgrep -af simulation.test",
				holder, timeout.Round(time.Second))
		}
		time.Sleep(runLockPollInterval)
	}

	// 锁文件内容只是诊断信息：写入当前 PID 与开始时间（截断后重写）。
	if err := writeLockHolder(file); err != nil {
		// 写不进诊断信息不影响互斥正确性（锁已由 flock 持有），只记不致命。
		// 但绝不能吞掉：预留 stderr 一行，让人知道诊断信息为何是旧的。
		fmt.Fprintf(os.Stderr, "场景仿真警告：写入锁持有者信息失败（互斥仍生效）: %v\n", err)
	}
	return &runLock{file: file, path: path}, nil
}

// writeLockHolder 把当前进程写进锁文件（诊断用，见 runLock 注释）。
func writeLockHolder(file *os.File) error {
	body := fmt.Sprintf("pid=%d\nstarted_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.WriteString(body); err != nil {
		return err
	}
	// 先 Write 后 Sync：崩溃后残留的内容也应当是完整的诊断信息。
	return file.Sync()
}

// describeLockHolder 读取持有者信息，渲染成"PID x，已持有 y"。
// 任何读取/解析失败都退化为可读的兜底文案，绝不因为诊断信息缺失而让错误信息变模糊。
func describeLockHolder(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return "持有者信息不可读（" + path + "）"
	}
	pid, duration := "", ""
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "pid="); ok && pid == "" {
			pid = value
		}
		if value, ok := strings.CutPrefix(line, "started_at="); ok && duration == "" {
			if started, parseErr := time.Parse(time.RFC3339, value); parseErr == nil {
				duration = time.Since(started).Round(time.Second).String()
			}
		}
	}
	if pid == "" {
		return "持有者信息不完整（" + path + "）"
	}
	if duration == "" {
		return "PID " + pid
	}
	return "PID " + pid + "，已持有 " + duration
}

// release 释放锁。**幂等**：重复调用、Close 失败后再调用都安全。
//
// 内核在进程退出时会自动释放，因此这里显式释放的价值是"同一进程内提前让位"
// （例如 Start 失败后立刻退出、或测试内多次获取），而不是防死锁。
func (l *runLock) release() {
	if l == nil || l.file == nil {
		return
	}
	file := l.file
	l.file = nil
	// 先解锁再关闭；即使 flock 返回错误也继续 Close（Close 本身会释放锁）。
	// 内容保留：下一个持有者会覆写，人工诊断时"上一个持有者是谁"仍有价值。
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

// ---------- 孤儿场景库清扫 ----------

// orphanRunDBPattern 匹配 **harness 自己生成的** 场景库名。
//
// 形态推导（不要凭印象改这个正则，它必须与 RunID/库名生成规则严格同步）：
//
//	Options.withDefaults: RunID = "sim-" + UTC 日期(20060102) + "-" + randHex(2)
//	                      → sim-20260912-a8d7          （默认形态）
//	Options.RunID 自定义: 任意字符串（调用方显式指定，harness 不得假定其形态）
//	DatabaseNameForRun:   非 [a-z0-9_] 折叠为 "_"，再拼 "ehome_sim_" 前缀
//	                      → ehome_sim_sim_20260912_a8d7（默认形态）
//
// 因此"harness 自己生成的库"= ehome_sim_sim_<8位日期>_<4位十六进制>。
//
// ⚠ 为什么**绝不能**用宽谓词（例如 "^ehome_sim_[a-z0-9_]+$"，也就是 §7-1 的安全红线正则）：
// 那个前缀是设计 §7-1 给**人**划定的合法命名空间，不是删除凭据。
// 用它做清扫 = 任何人按文档起的合法库名（实测：手工侦察库 ehome_sim_recon）
// 都会在某次 run 启动时被静默 DROP —— 安全机制被误用成破坏机制。
// 宁可漏删（留下一个孤儿库，人工一条 SQL 即可清理），不可误删（数据不可恢复）。
var orphanRunDBPattern = regexp.MustCompile("^ehome_sim_sim_[0-9]{8}_[0-9a-f]{4}$")

// cleanupOrphanDbs 在**已持有排他锁**的前提下，删除 harness 自己生成、
// 且不属于当前 run 的场景库。
//
// 为什么必须在持锁之后：
//  1. 此刻已确认没有其他 run 在跑，这些库不可能还有活着的所有者；
//  2. 不持锁就删库会把**正在运行**的另一个 run 的场景库删掉（它的服务子进程
//     会随之中断或写入已删除的库），是比并发污染更直接的破坏。
//
// 为什么需要它：run 收尾 DROP 依赖 t.Cleanup 执行到最后一行；进程被 SIGKILL /
// panic / 手工 Ctrl-C 时库会留下来。此前手工清理过 19 个孤儿库。
//
// 匹配不到的库一律跳过，并且**列进日志**（"疑似手工库，未清理"）——
// 静默跳过会让人以为清扫覆盖了全部，而实际留下的是需要人处理的东西。
//
// 失败只记日志不致命：清扫是卫生工作，不能因为它把一次正常的 run 弄挂。
func cleanupOrphanDbs(t *testing.T, admin *databaseAdmin, keep string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	names, err := admin.ListSimulationDatabases(ctx)
	if err != nil {
		t.Logf("清理孤儿场景库：列出场景库失败（已忽略）: %v", err)
		return
	}
	dropped := make([]string, 0, len(names))
	skipped := make([]string, 0, len(names))
	for _, name := range names {
		if name == keep {
			skipped = append(skipped, name)
			continue
		}
		if !orphanRunDBPattern.MatchString(name) {
			// 自定义 RunID（Options.RunID）产生的库也会走到这里：harness 无法
			// 判断它是否仍被需要，因此一律不碰 —— 宁可漏删，不可误删。
			skipped = append(skipped, name)
			continue
		}
		if err := admin.DropDatabase(ctx, name); err != nil {
			t.Logf("清理孤儿场景库：DROP %s 失败（已忽略）: %v", name, err)
			skipped = append(skipped, name)
			continue
		}
		dropped = append(dropped, name)
	}

	if len(dropped) > 0 {
		t.Logf("孤儿场景库清理：已删除 %d 个 harness 自己生成、且所有者进程已不存在的库 %v",
			len(dropped), dropped)
	} else {
		t.Logf("孤儿场景库清理：无可清理的 harness 生成库（当前 run 的库 %s 保留）", keep)
	}
	if len(skipped) > 0 {
		t.Logf("孤儿场景库清理：跳过 %d 个库（当前 run 的库 + 疑似手工库，均未清理）: %v",
			len(skipped), skipped)
	}
}
