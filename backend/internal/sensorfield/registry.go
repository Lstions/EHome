package sensorfield

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/pkg/logger"
)

// snapshot 是不可变的读快照（atomic.Pointer 发布，读路径无锁）。
//
// 变更是“整体替换”：任一来源变化 ⇒ publishLocked 构建新 snapshot 且 epoch+1。
type snapshot struct {
	epoch       uint64
	built       time.Time
	enabled     bool
	decls       map[string][]Field        // device_type -> 声明（顺序保留）
	obs         map[uint]map[string]Field // deviceID(= EdgeDevice.ID) -> 观测
	meta        map[string]Field          // sensor_name -> 丰富层（全局）
	deviceTypes map[uint]string           // deviceID -> device_type
	aliases     *aliasTable               // 不可变
	virtuals    map[string]*virtualNode   // 不可变
}

// compileKey 是编译缓存键（设备 + 归一后的 selector 原文）。
type compileKey struct {
	deviceID uint
	selector string
}

// compileCacheEntry 是编译缓存条目（含编译时刻，用于 SnapshotTTL 兜底）。
type compileCacheEntry struct {
	plan Plan
	at   time.Time
}

// Registry 是数据字段解析层的内存注册表（方案 §4.1）。
//
// 并发契约：
//   - 读路径（Compile/Resolve/KnownFields/Enabled/Epoch/SemanticsOf）无锁读 atomic snapshot；
//   - 写路径（注册/刷新/配置替换）走 mu，构建新 snapshot 后原子发布；
//   - Observe 完全绕开 mu（sync.Map + atomic），热路径无锁；
//   - Plan 不可变且并发安全，可被多 shard 同时 Eval。
type Registry struct {
	cfg     Config
	enabled atomic.Bool
	snap    atomic.Pointer[snapshot]

	epochCounter atomic.Uint64

	mu          sync.Mutex
	decls       map[string][]Field
	obs         map[uint]map[string]Field
	meta        map[string]Field
	deviceTypes map[uint]string
	aliasRules  []AliasRule
	aliases     *aliasTable
	virtualDefs []VirtualDef
	virtuals    map[string]*virtualNode

	// frozenAlias 冻结 (device, semantic) -> 首次解析到的物理名（§6.1 规则 4）。
	frozenAlias sync.Map // map[frozenAliasKey]string
	// aliasWarned 是“非首候选/多候选”一次性通知去重（§5.4）。
	aliasWarned sync.Map // map[aliasWarnKey]struct{}

	observe *obsStore
	flushMu sync.Mutex

	cacheMu sync.Mutex
	cache   map[compileKey]*compileCacheEntry
}

// New 构造 Registry。Config 零值字段取默认值；Enabled 零值 = 关闭（kill switch 默认关）。
func New(cfg Config) *Registry {
	r := &Registry{
		cfg:         defaultConfig(cfg),
		decls:       map[string][]Field{},
		obs:         map[uint]map[string]Field{},
		meta:        map[string]Field{},
		deviceTypes: map[uint]string{},
		aliases:     newEmptyAliasTable(),
		virtuals:    map[string]*virtualNode{},
		observe:     newObsStore(),
		cache:       map[compileKey]*compileCacheEntry{},
	}
	r.enabled.Store(cfg.Enabled)
	r.publishLocked()
	return r
}

// publishLocked 构建并原子发布新 snapshot（epoch+1）。调用方必须持有 r.mu。
func (r *Registry) publishLocked() *snapshot {
	epoch := r.epochCounter.Add(1)
	s := &snapshot{
		epoch:       epoch,
		built:       time.Now(),
		enabled:     r.enabled.Load(),
		decls:       cloneDecls(r.decls),
		obs:         cloneObs(r.obs),
		meta:        cloneMeta(r.meta),
		deviceTypes: cloneDeviceTypes(r.deviceTypes),
		aliases:     r.aliases,
		virtuals:    r.virtuals,
	}
	r.snap.Store(s)
	return s
}

// Enabled 报告开关状态（kill switch：关闭时特殊 selector 一律拒绝，字面量不变）。
func (r *Registry) Enabled() bool { return r.enabled.Load() }

// SetEnabled 切换开关。
//
// ⚠ 仅供测试/运维（含 kill switch 联调）；**配置变更仍以“改配置 + 重启进程”生效为准**
// （方案 §11：仓库没有热加载机制，不新增 watcher）。运行期切换只影响后续
// Compile/Resolve 与已编译特殊 Plan 的 Eval（每次求值读原子开关）。
func (r *Registry) SetEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.enabled.Swap(enabled) == enabled {
		return
	}
	r.publishLocked()
}

// Epoch 返回当前快照版本号（任一来源变更 +1）。
func (r *Registry) Epoch() uint64 {
	if s := r.snap.Load(); s != nil {
		return s.epoch
	}
	return 0
}

// SnapshotAge 返回当前快照年龄（方案 §10：由 Flush 周期性刷新为“刷新前的年龄”）。
func (r *Registry) SnapshotAge() time.Duration {
	s := r.snap.Load()
	if s == nil {
		return 0
	}
	return time.Since(s.built)
}

// Config 返回解析层生效配置（含默认值），供 /schema 端点展示 limits。
func (r *Registry) Config() Config { return r.cfg }

// RegisterDeclarations 注册某设备类型的静态声明（整体替换该类型）。
//
// 声明来源：drivers.Registry 的 GetSensorDefinitions()（单值 Field，ValueType 通常未知）。
// 非法物理名/空名静默跳过并 WARN（声明是启动期可信输入，不打断启动）。
func (r *Registry) RegisterDeclarations(deviceType string, fields []Field) {
	dt := strings.TrimSpace(deviceType)
	if dt == "" {
		logger.Warn("sensorfield: RegisterDeclarations with empty device_type ignored")
		return
	}
	clean := make([]Field, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		name := strings.TrimSpace(f.Name)
		if !isPhysicalName(name) {
			logger.Warn("sensorfield: declaration field with invalid physical name skipped",
				"device_type", dt, "name", f.Name)
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		f.Name = name
		f.Declared = true
		f.Observed = false
		clean = append(clean, f)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decls[dt] = clean
	r.publishLocked()
}

// SetDeviceType 绑定设备 ID 到设备类型（声明来源的归属依据）。
//
// 为什么必须新增（冻结契约缺口）：RegisterDeclarations 按 device_type 注册，
// 而 Resolve/Compile/KnownFields 只有 deviceID；没有本方法“声明 ∪ 观测”无法成立。
// deviceID 语义 = EdgeDevice.ID = unified_data.device_id（禁用 edge_device_id，DS-14）。
// deviceType 变化时 epoch+1。
func (r *Registry) SetDeviceType(deviceID uint, deviceType string) {
	if deviceID == 0 {
		return
	}
	dt := strings.TrimSpace(deviceType)
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.deviceTypes[deviceID]; ok && old == dt {
		return
	}
	if dt == "" {
		delete(r.deviceTypes, deviceID)
	} else {
		r.deviceTypes[deviceID] = dt
	}
	r.publishLocked()
}

// SetObservations 整体替换某设备的观测集合（冷启动 / 投影刷新）。
//
// 来源：sensor_field_obs 投影表（M2/M3）。字段名非法/重复的处理与声明一致。
func (r *Registry) SetObservations(deviceID uint, fields []Field) {
	if deviceID == 0 {
		return
	}
	clean := make(map[string]Field, len(fields))
	for _, f := range fields {
		name := strings.TrimSpace(f.Name)
		if !isPhysicalName(name) {
			logger.Warn("sensorfield: observation field with invalid physical name skipped",
				"device_id", deviceID, "name", f.Name)
			continue
		}
		if _, dup := clean[name]; dup {
			continue
		}
		f.Name = name
		f.Observed = true
		f.Declared = false
		clean[name] = f
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(clean) == 0 {
		delete(r.obs, deviceID)
	} else {
		r.obs[deviceID] = clean
	}
	r.publishLocked()
}

// SetFieldMeta 整体替换丰富层元数据（按 sensor_name 全局生效）。
//
// 三源合并优先级（已知集合组装时）：meta > obs > decl（“最具体”来源优先）；
// 仅 Unit/ValueType 受 meta 影响，LastSeen/Samples 仍只来自观测。
func (r *Registry) SetFieldMeta(fields []Field) {
	clean := make(map[string]Field, len(fields))
	for _, f := range fields {
		name := strings.TrimSpace(f.Name)
		if !isPhysicalName(name) {
			logger.Warn("sensorfield: field meta with invalid physical name skipped", "name", f.Name)
			continue
		}
		if f.ValueType != TypeUnknown && !isKnownValueType(f.ValueType) {
			logger.Warn("sensorfield: field meta with unknown value_type skipped",
				"name", name, "value_type", f.ValueType)
			continue
		}
		f.Name = name
		f.Declared = false
		f.Observed = false
		clean[name] = f
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.meta = clean
	r.publishLocked()
}

// isKnownValueType 报告 t 是否是 ValueType 白名单取值。
func isKnownValueType(t ValueType) bool {
	switch t {
	case TypeUnknown, TypeNumber, TypeBoolean, TypeEnum, TypeBitmask, TypeString, TypeRaw:
		return true
	}
	return false
}

// KnownFields 返回该设备已知字段（声明 ∪ 观测，meta 丰富），带 Declared/Observed 标志。
// 关闭态仍可读（/sensor-fields 与 /schema 在 kill switch 下要可用）。
func (r *Registry) KnownFields(deviceID uint) []Field {
	s := r.snap.Load()
	if s == nil {
		return nil
	}
	return s.knownFields(deviceID)
}

// SemanticsOf 返回某物理名归属的语义名列表（配置顺序）。
func (r *Registry) SemanticsOf(physical string) []string {
	s := r.snap.Load()
	if s == nil {
		return nil
	}
	return s.aliases.semanticsOf(physical)
}

// Aliases 返回当前别名规则副本（/schema 端点用；关闭态仍可读）。
func (r *Registry) Aliases() []AliasRule {
	r.mu.Lock()
	rules := cloneAliasRules(r.aliasRules)
	r.mu.Unlock()
	return rules
}

// Virtuals 返回当前派生定义副本（/schema 端点用；关闭态仍可读）。
func (r *Registry) Virtuals() []VirtualDef {
	r.mu.Lock()
	defs := cloneVirtualDefs(r.virtualDefs)
	r.mu.Unlock()
	return defs
}

// SetAliases 校验并替换别名表（配置按 key 整体替换，不追加）。
//
// 失败时不改动当前表（fail-closed：调用方应拒绝启动/拒绝保存）。
func (r *Registry) SetAliases(rules []AliasRule) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	declared := r.unitClassesLocked()
	table, err := buildAliasTable(rules, declared)
	if err != nil {
		return err
	}
	r.aliasRules = cloneAliasRules(rules)
	r.aliases = table
	r.publishLocked()
	return nil
}

// SetVirtuals 校验并替换派生定义集合（整体替换）。
//
// 失败时不改动当前定义集。环/深度/参数类型错误都是配置错误（fail-closed，方案 §6.2）。
func (r *Registry) SetVirtuals(defs []VirtualDef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	nodes, err := buildVirtuals(defs, r.cfg, r.aliases)
	if err != nil {
		return err
	}
	r.virtualDefs = cloneVirtualDefs(defs)
	r.virtuals = nodes
	r.publishLocked()
	return nil
}

// Validate 复核配置整体自洽（方案 §6.1/§6.2/§12.1 的启动期 fail-closed 落点）。
//
// 必须在“声明/观测/元数据/别名/派生全部装载之后”调用：
//  1. 别名表重建（冲突 + 单位类）
//  2. 死候选检测（评审 H1）：候选必须出现在**任何**声明或观测里
//  3. 派生定义重建（参数个数/类型/环/深度 + @semantic 交叉引用）
//
// 与 SetAliases/SetVirtuals 的区别：那两个方法只做单表自洽校验（可能因装载顺序
// 看不到对方），Validate 做跨表校验，是启动期唯一权威门禁。
func (r *Registry) Validate() error {
	r.mu.Lock()
	rules := cloneAliasRules(r.aliasRules)
	defs := cloneVirtualDefs(r.virtualDefs)
	declared := r.unitClassesLocked()
	knownNames := r.knownNamesLocked()
	curAliases := r.aliases
	r.mu.Unlock()

	var errs []error
	table, err := buildAliasTable(rules, declared)
	if err != nil {
		errs = append(errs, err)
	} else if dead := table.deadCandidates(knownNames); len(dead) > 0 {
		errs = append(errs, fmt.Errorf("%w: dead alias candidates not produced by any declaration/observation: %v", ErrAliasConflict, dead))
	}
	aliasForVirtual := table
	if aliasForVirtual == nil {
		aliasForVirtual = curAliases
	}
	if _, err := buildVirtuals(defs, r.cfg, aliasForVirtual); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// unitClassesLocked 汇总已知单位类（声明 + 观测 + 元数据），供别名单位类校验。
// 调用方必须持有 r.mu。
func (r *Registry) unitClassesLocked() map[string]map[unitClass]struct{} {
	out := map[string]map[unitClass]struct{}{}
	add := func(f Field) {
		c := classOfUnit(f.Unit)
		if c == unitClassUnknown {
			return
		}
		set := out[f.Name]
		if set == nil {
			set = map[unitClass]struct{}{}
			out[f.Name] = set
		}
		set[c] = struct{}{}
	}
	for _, fields := range r.decls {
		for _, f := range fields {
			add(f)
		}
	}
	for _, observed := range r.obs {
		for _, f := range observed {
			add(f)
		}
	}
	for _, f := range r.meta {
		add(f)
	}
	return out
}

// knownNamesLocked 汇总所有“声明/观测里出现过的物理名”（死候选检测用）。
// 调用方必须持有 r.mu。
func (r *Registry) knownNamesLocked() map[string]struct{} {
	out := map[string]struct{}{}
	for _, fields := range r.decls {
		for _, f := range fields {
			out[f.Name] = struct{}{}
		}
	}
	for _, observed := range r.obs {
		for name := range observed {
			out[name] = struct{}{}
		}
	}
	return out
}

// Observe 记录一次观测（纯内存，热路径）。data-plane 由 main.go 在既有
// SetLatestSinkFn 注入点包装调用（红线 1：databus 零 import）。
func (r *Registry) Observe(deviceID uint, name, unit string, at time.Time) {
	r.observe.observe(deviceID, name, unit, at)
}

// Flush 把观测增量交给 FlushSink 落库（方案 §4.2 projector，调用方每 observe_interval 一次）。
//
// 语义：
//   - 只交出“自上次成功 Flush 以来的增量”（samples 为增量，first/last 为进程内极值）
//   - sink 失败 ⇒ 增量保留，下次重试；不合并进内存已知集合（降级“仅声明”，§12.1）
//   - sink == nil ⇒ 跳过落库但合并内存（测试/DB 不可用的降级路径）
//   - 成功后合并内存已知集合并发布新快照（G-1：首帧后 ≤90s 可见）
//   - 同时刷新 ehome_field_snapshot_age_seconds（刷新前快照年龄）
func (r *Registry) Flush(ctx context.Context, sink FlushSink) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s := r.snap.Load(); s != nil {
		FieldSnapshotAgeSeconds.Set(time.Since(s.built).Seconds())
	}
	entries := r.observe.collectPending()
	if len(entries) == 0 {
		return nil
	}
	obs := flushEntriesToObservations(entries)
	if sink != nil {
		if err := sink.UpsertObservations(obs); err != nil {
			return fmt.Errorf("sensorfield: flush observations: %w", err)
		}
	}
	ackPending(entries)
	r.mergeObservations(obs)
	return nil
}

// mergeObservations 把已落库的观测增量合并进内存快照（epoch+1）。
func (r *Registry) mergeObservations(obs []Observation) {
	if len(obs) == 0 {
		return
	}
	counts := make(map[uint]int, 4)
	r.mu.Lock()
	for _, o := range obs {
		if o.DeviceID == 0 || o.Name == "" {
			continue
		}
		byName := r.obs[o.DeviceID]
		if byName == nil {
			byName = map[string]Field{}
			r.obs[o.DeviceID] = byName
		}
		cur, exists := byName[o.Name]
		if !exists {
			cur = Field{Name: o.Name}
		}
		cur.Observed = true
		if o.Unit != "" {
			cur.Unit = o.Unit
		}
		if o.LastSeen.After(cur.LastSeen) {
			cur.LastSeen = o.LastSeen
		}
		cur.Samples += o.Samples
		byName[o.Name] = cur
		counts[o.DeviceID]++
	}
	r.publishLocked()
	labels := make(map[string]int, len(counts))
	for deviceID, n := range counts {
		label := r.deviceTypes[deviceID]
		if label == "" {
			label = "unknown"
		}
		labels[label] += n
	}
	r.mu.Unlock()
	for label, n := range labels {
		FieldObservedTotal.WithLabelValues(label).Add(float64(n))
	}
}

// lookupCompiled 命中编译缓存（epoch 一致 + TTL 内）。
func (r *Registry) lookupCompiled(key compileKey) (Plan, bool) {
	s := r.snap.Load()
	if s == nil {
		return nil, false
	}
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	entry := r.cache[key]
	if entry == nil {
		return nil, false
	}
	if PlanEpoch(entry.plan) != s.epoch || time.Since(entry.at) >= r.cfg.SnapshotTTL {
		delete(r.cache, key)
		return nil, false
	}
	return entry.plan, true
}

// storeCompiled 写入编译缓存（超过上限整体清空，防无界增长）。
func (r *Registry) storeCompiled(key compileKey, plan Plan) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if len(r.cache) >= maxCompileCacheEntries {
		r.cache = map[compileKey]*compileCacheEntry{}
	}
	r.cache[key] = &compileCacheEntry{plan: plan, at: time.Now()}
}

// ---- clone helpers（发布快照时复制，防写路径与读路径共享可变结构） ----

func cloneDecls(in map[string][]Field) map[string][]Field {
	out := make(map[string][]Field, len(in))
	for k, v := range in {
		cp := make([]Field, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func cloneObs(in map[uint]map[string]Field) map[uint]map[string]Field {
	out := make(map[uint]map[string]Field, len(in))
	for k, v := range in {
		cp := make(map[string]Field, len(v))
		for name, f := range v {
			cp[name] = f
		}
		out[k] = cp
	}
	return out
}

func cloneMeta(in map[string]Field) map[string]Field {
	out := make(map[string]Field, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneDeviceTypes(in map[uint]string) map[uint]string {
	out := make(map[uint]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneAliasRules(in []AliasRule) []AliasRule {
	if len(in) == 0 {
		return nil
	}
	out := make([]AliasRule, len(in))
	for i, r := range in {
		out[i] = r
		out[i].Candidates = append([]string(nil), r.Candidates...)
	}
	return out
}

func cloneVirtualDefs(in []VirtualDef) []VirtualDef {
	if len(in) == 0 {
		return nil
	}
	out := make([]VirtualDef, len(in))
	for i, d := range in {
		out[i] = d
		out[i].Args = append([]string(nil), d.Args...)
	}
	return out
}
