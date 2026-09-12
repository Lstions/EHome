//go:build simulation

// Package harness 是场景仿真验证框架的地基：它编译并启动真实的
// ./cmd/server 组合根，提供真实 HTTP / MQTT / PG 之上的黑盒断言原语。
//
// 本文件负责「场景库」的生命周期与安全边界。设计 §7-1 是硬红线：
// harness 只允许 CREATE/DROP 名称匹配 ^ehome_sim_[a-z0-9_]+$ 的库，
// ehome / ehome_test / 任何线上库一律拒绝连接。该红线在此以正则常量 +
// 每次连接前的 ValidateDatabaseName 机器化执行——不依赖调用方自律。
package harness

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// simDBPattern 是安全红线的机器化表达（设计 §7-1）。
// 只允许小写字母、数字与下划线，且必须以 ehome_sim_ 开头：
// "ehome"、"ehome_test"、"ehome_sim" 等一律不匹配。
var simDBPattern = regexp.MustCompile("^ehome_sim_[a-z0-9_]+$")

// maintenanceDatabase 是执行 CREATE/DROP DATABASE 的维护库。
// 它只承载 DDL，不承载任何业务读写。
const maintenanceDatabase = "postgres"

// ValidateDatabaseName 在建立任何连接之前校验库名。
// 这是设计 §7-1 的唯一执行点：任何绕过它的连接都视为红线违规。
func ValidateDatabaseName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("场景库名为空：拒绝连接（安全红线，设计 §7-1）")
	}
	if !simDBPattern.MatchString(name) {
		return fmt.Errorf("拒绝操作数据库 %q：库名必须匹配 %s（安全红线，设计 §7-1）", name, simDBPattern.String())
	}
	return nil
}

// DatabaseNameForRun 由 RunID 派生场景库名。
//
// 设计 §5.1 规定库名为 "ehome_sim_<runid>"，同时 RunID 缺省形如
// "sim-<时间戳>-<4位随机>" —— 两者直接拼接会得到含 "-" 的库名，
// 与 §7-1 的 ^ehome_sim_[a-z0-9_]+$ 冲突。此处按 §7-1（红线优先）
// 把非 [a-z0-9_] 字符折叠为 "_"，再做一次 ValidateDatabaseName 兜底。
func DatabaseNameForRun(runID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(runID) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return "ehome_sim_" + b.String()
}

// databaseAdmin 拥有场景库的建/删能力。它只连维护库 postgres，
// 因此即便实现对 DSN 处理有误，也不可能误写业务库的表。
type databaseAdmin struct {
	config *pgx.ConnConfig // 指向目标场景库（仅用于取参数，连接的是维护库）
	db     *sql.DB         // 维护库连接
}

// newDatabaseAdmin 解析 DSN、校验目标库名并连接维护库。
// 失败信息面向"运维可执行"：明确指出 PG 地址与 make infra 提示。
func newDatabaseAdmin(ctx context.Context, dsn string) (*databaseAdmin, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析 EHOME_DB_* DSN 失败: %w", err)
	}
	// 先校验目标库名，再建立任何连接：不合规直接失败，绝不"连上再说"。
	if err := ValidateDatabaseName(cfg.Database); err != nil {
		return nil, err
	}
	adminCfg := cfg.Copy()
	adminCfg.Database = maintenanceDatabase

	db := stdlib.OpenDB(*adminCfg)
	pingCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("PostgreSQL 不可达（%s:%d，维护库 %s）: %v；请先执行 make infra",
			cfg.Host, cfg.Port, maintenanceDatabase, err)
	}
	return &databaseAdmin{config: cfg, db: db}, nil
}

// ScenarioDSN 返回指向场景库的连接串（供诊断打印，不用于连接）。
func (a *databaseAdmin) ScenarioDSN() string {
	cfg := a.config.Copy()
	return cfg.ConnString()
}

func (a *databaseAdmin) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	return a.db.Close()
}

// EnsureDatabase 实现设计 §5.1 启动序列第 1~2 步：库已存在则先 DROP，
// 再 CREATE。DROP ... WITH (FORCE) 会断开残留连接，使重复运行幂等。
func (a *databaseAdmin) EnsureDatabase(ctx context.Context, name string) error {
	if err := ValidateDatabaseName(name); err != nil {
		return err
	}
	if err := a.DropDatabase(ctx, name); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", quoteIdentifier(name))); err != nil {
		return fmt.Errorf("创建场景库 %s 失败: %w", name, err)
	}
	return nil
}

// DropDatabase 删除场景库。不存在时静默成功（幂等）。
func (a *databaseAdmin) DropDatabase(ctx context.Context, name string) error {
	if err := ValidateDatabaseName(name); err != nil {
		return err
	}
	if _, err := a.db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", quoteIdentifier(name))); err != nil {
		return fmt.Errorf("删除场景库 %s 失败: %w", name, err)
	}
	return nil
}

// OpenScenarioDatabase 打开场景库连接，供只读断言与轮询收敛条件使用
// （设计 §3 原则 2：直连 DB 只用于界面看不到的持久化事实与轮询条件，
// 不得替代本可走 API 的断言）。
func (a *databaseAdmin) OpenScenarioDatabase(ctx context.Context) (*sql.DB, error) {
	// 打开前再校验一次：防止配置在运行期被改写。
	if err := ValidateDatabaseName(a.config.Database); err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*a.config)
	pingCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接场景库 %s 失败: %w", a.config.Database, err)
	}
	return db, nil
}

// ListSimulationDatabases 列出当前实例上全部 ehome_sim_* 场景库。
//
// 用途：harness 持有 run 级排他锁之后清扫孤儿库（进程被 SIGKILL 时
// t.Cleanup 的 DROP 不会执行，库会残留）。
//
// 两道收窄，缺一不可：
//  1. SQL 层只查 simDBPattern 的字面前缀，绝不把整个 pg_database 拉回来；
//  2. Go 层再用 simDBPattern 全量校验每个名字 —— 前缀匹配会放进
//     "ehome_sim" 本身这类不满足红线（`[a-z0-9_]+` 至少要一个字符）的库名，
//     而 DROP 路径的红线校验必须与 CREATE 路径完全一致。
func (a *databaseAdmin) ListSimulationDatabases(ctx context.Context) ([]string, error) {
	const prefix = "ehome_sim_"
	rows, err := a.db.QueryContext(ctx,
		"SELECT datname FROM pg_database WHERE datname LIKE $1 ORDER BY datname", prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("列出场景库失败: %w", err)
	}
	defer rows.Close()

	names := make([]string, 0, 8)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("读取场景库名失败: %w", err)
		}
		if simDBPattern.MatchString(name) {
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历场景库列表失败: %w", err)
	}
	return names, nil
}

// quoteIdentifier 用双引号包裹标识符。库名已由 ValidateDatabaseName
// 限定为 [a-z0-9_]，不存在注入面；加引号只是防御性写法。
func quoteIdentifier(name string) string {
	return "\"" + strings.ReplaceAll(name, "\"", "") + "\""
}
