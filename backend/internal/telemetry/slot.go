package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ehome/backend/internal/models"
)

// SlotField 是一个字段在 slot 里的定义：物理名 + **写入时的**量化规格快照。
//
// 为什么把规格内联在 slot 里（而不是每次解码现查 spec.go）：
// 见 SpecVersion 注释 —— 量化规格会演进，历史帧必须按写入时的规格解，
// 否则一次步长调整就把整段历史变成错值。spec.go 是**写入侧**的默认值来源，
// slot 是**读取侧**的真值来源。
type SlotField struct {
	Name string  `json:"name"`
	Min  float64 `json:"min"`
	Step float64 `json:"step"`
	Bits uint8   `json:"bits"`
}

// Spec 把 SlotField 还原成 FieldSpec。
func (f SlotField) Spec() FieldSpec {
	return FieldSpec{Min: f.Min, Step: f.Step, Bits: f.Bits}
}

// Slot 是一个帧类型：字段名列表（**顺序即位流顺序**）+ 量化规格快照 + 版本。
//
// 顺序不是字典序而是 encoder 传入的顺序吗？—— 是**字典序**：
// 同一帧的字段在解析结果里的顺序依赖驱动的字段发射顺序，那东西会随固件/驱动
// 演进而变；把顺序钉成字典序，才能让"字段集合相同的两帧"稳定命中同一 slot。
// 代价是需要把物理量按名重排（encoder 负责），换来 slot 的稳定与可复现。
type Slot struct {
	// ID 持久化后的 slot_id（0 = 尚未落库，仅内存解析）。
	ID int16
	// Signature 字典键（见 SignatureOf）。
	Signature string
	// CommandTemplateID 产出该帧的指令模板 ID（0 = 非命令路径）。
	CommandTemplateID int64
	// SpecVersion 量化规格版本。
	SpecVersion uint16
	// Fields 字段定义（已按名字排序）。
	Fields []SlotField
}

// NumFields 返回字段数。
func (s *Slot) NumFields() int {
	if s == nil {
		return 0
	}
	return len(s.Fields)
}

// Names 返回字段名列表（副本）。
func (s *Slot) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		out[i] = f.Name
	}
	return out
}

// Specs 返回字段量化规格列表（顺序与字段一致）。
func (s *Slot) Specs() []FieldSpec {
	if s == nil {
		return nil
	}
	out := make([]FieldSpec, len(s.Fields))
	for i, f := range s.Fields {
		out[i] = f.Spec()
	}
	return out
}

// indexOf 返回字段名到下标的下标表。
func (s *Slot) indexOf() map[string]int {
	m := make(map[string]int, len(s.Fields))
	for i, f := range s.Fields {
		m[f.Name] = i
	}
	return m
}

// SignatureOf 计算 slot 签名。
//
// 组成：command_template_id | spec_version | 排序后的字段名列表。
//
// 为什么这三样、而不是"设备型号"：
//   - 设备型号是**粗粒度**：同一台 device15 的 7 种帧类型都来自同一型号，
//     用它做键会把 7 种帧合成一个 slot ⇒ 写串（方案 §4.4 的 ⚠ 就是这条）；
//   - command_template_id 是**指令级**粒度，实测正好一一对应 7 种帧；
//   - 字段名集合是最后一道保险：即便指令模板复用，字段集合不同也不会撞。
//
// 不含设备 ID：同一型号的多个设备共享 slot（字段集合与精度相同），
// 不同的只是行里的 device_id —— 这是 slot 字典能只有几十行的原因。
func SignatureOf(commandTemplateID int64, specVersion uint16, fieldNames []string) string {
	sorted := make([]string, len(fieldNames))
	copy(sorted, fieldNames)
	sort.Strings(sorted)
	h := sha256.New()
	fmt.Fprintf(h, "%d|%d|", commandTemplateID, specVersion)
	h.Write([]byte(strings.Join(sorted, ",")))
	// 截断到 32 hex（128 位）：碰撞概率对"几十个 slot"这个量级完全可忽略，
	// 而 char(64) 的唯一索引在生产上每行省 32 B。
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// NewSlot 按字段名与规格表构造 slot（未落库，ID=0）。
//
// 返回 error 的情况：字段名为空、字段重名、规格非法。这些都是**编程错误**
// （不是数据问题），写路径必须让它们响，而不是构造出一个坏 slot。
func NewSlot(commandTemplateID int64, specVersion uint16, names []string) (*Slot, error) {
	if len(names) == 0 {
		return nil, ErrNoFields
	}
	if len(names) > MaxSlotFields {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooManyFields, len(names), MaxSlotFields)
	}
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	seen := make(map[string]struct{}, len(sorted))
	fields := make([]SlotField, 0, len(sorted))
	for _, n := range sorted {
		if n == "" {
			return nil, fmt.Errorf("telemetry: empty field name in slot")
		}
		if _, dup := seen[n]; dup {
			return nil, fmt.Errorf("telemetry: duplicate field %q in slot", n)
		}
		seen[n] = struct{}{}
		sp, _ := SpecFor(n) // 未登记字段用 FallbackSpec，由 UnknownFields 指标暴露
		if !sp.Valid() {
			return nil, fmt.Errorf("telemetry: invalid spec for field %q: %+v", n, sp)
		}
		fields = append(fields, SlotField{Name: n, Min: sp.Min, Step: sp.Step, Bits: sp.Bits})
	}
	return &Slot{
		Signature:         SignatureOf(commandTemplateID, specVersion, sorted),
		CommandTemplateID: commandTemplateID,
		SpecVersion:       specVersion,
		Fields:            fields,
	}, nil
}

// MarshalFields 把字段快照序列化成 TelemetrySlot.FieldsJSON 的内容。
func (s *Slot) MarshalFields() (string, error) {
	b, err := json.Marshal(s.Fields)
	if err != nil {
		return "", fmt.Errorf("telemetry: marshal slot fields: %w", err)
	}
	return string(b), nil
}

// UnmarshalFields 从 TelemetrySlot.FieldsJSON 还原字段快照。
func UnmarshalFields(raw string) ([]SlotField, error) {
	var fields []SlotField
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("telemetry: unmarshal slot fields: %w", err)
	}
	for i, f := range fields {
		if !f.Spec().Valid() {
			return nil, fmt.Errorf("telemetry: slot field %d (%q) has invalid spec: %+v", i, f.Name, f.Spec())
		}
	}
	return fields, nil
}

// SlotDictionary 是 slot 的解析与持久化入口。
//
// 语义：Resolve(names) 给一个"帧的字段集合"，返回**稳定**的 slot（同一集合
// 在同一 command_template_id 下永远得到同一 slot_id）。实现必须并发安全
// （写路径是多 shard 并发的）。
type SlotDictionary interface {
	// Resolve 返回字段集合对应的 slot；首次见到该集合时落库并分配 ID。
	Resolve(commandTemplateID int64, fieldNames []string) (*Slot, error)
	// ByID 按 slot_id 取 slot（解码路径用）。
	ByID(slotID int16) (*Slot, bool)
	// All 返回当前已知全部 slot（启动预热/诊断用）。
	All() []*Slot
}

// MemorySlots 是 SlotDictionary 的纯内存实现（无 DB）。
//
// 用途：
//   - 单测与真实数据往返测试（不需要数据库即可验证 slot 派生逻辑）；
//   - DB 不可用时的降级（slot 只在本进程内稳定）。
type MemorySlots struct {
	mu      sync.RWMutex
	bySig   map[string]*Slot
	byID    map[int16]*Slot
	nextID  int16
	persist func(*Slot) (int16, error)
}

// NewMemorySlots 构造纯内存槽字典。
func NewMemorySlots() *MemorySlots {
	return &MemorySlots{bySig: make(map[string]*Slot), byID: make(map[int16]*Slot), nextID: 1}
}

// Resolve 实现 SlotDictionary。
func (m *MemorySlots) Resolve(commandTemplateID int64, fieldNames []string) (*Slot, error) {
	fresh, err := NewSlot(commandTemplateID, SpecVersion, fieldNames)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	if s, ok := m.bySig[fresh.Signature]; ok {
		m.mu.RUnlock()
		return s, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.bySig[fresh.Signature]; ok { // 双检：并发首次见到同一集合
		return s, nil
	}
	if m.persist != nil {
		id, perr := m.persist(fresh)
		if perr != nil {
			return nil, perr
		}
		fresh.ID = id
	} else {
		fresh.ID = m.nextID
		m.nextID++
	}
	m.bySig[fresh.Signature] = fresh
	if fresh.ID > 0 {
		m.byID[fresh.ID] = fresh
	}
	return fresh, nil
}

// ByID 实现 SlotDictionary。
func (m *MemorySlots) ByID(slotID int16) (*Slot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.byID[slotID]
	return s, ok
}

// All 实现 SlotDictionary。
func (m *MemorySlots) All() []*Slot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Slot, 0, len(m.bySig))
	for _, s := range m.bySig {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// DBSlots 是 SlotDictionary 的持久化实现：内存缓存 + telemetry_slot 表。
//
// 读路径零查询：启动时 WarmUp 一次全量装载，之后 ByID 是 map 查找。
// 写路径只有**首次见到新字段集合**时写一行（几十行/年，不构成热路径成本）。
type DBSlots struct {
	mem  *MemorySlots
	db   *gorm.DB
	once sync.Once
	// 统计：解析到未在 spec.go 登记的字段次数（验收要求真实数据上为 0）。
	unknownFields int64
	unknownMu     sync.Mutex
}

// NewDBSlots 构造持久化槽字典（不立即查库；首次 Resolve/ByID 时可用 WarmUp）。
func NewDBSlots(db *gorm.DB) *DBSlots {
	d := &DBSlots{db: db}
	d.mem = NewMemorySlots()
	d.mem.persist = d.persist
	return d
}

// WarmUp 从 telemetry_slot 全量装载到内存（幂等）。
//
// 表不存在时**不报错**（静默返回空字典）。2026-11-07 L2 无条件启用后，生产上
// 这张表由 main.go 的启动迁移建出（datalifecycle.MigrateTelemetryFrameToPartitioned，
// 失败即 Fatal），因此本函数在生产不会遇到"表不存在"。静默返回只为覆盖两类
// 非生产场景：SQLite 单测库（按需自建）与直接调用本包的小工具。
//
// ⚠ 注意它**不是**失败闸门：真正的闸门是构造 DualWriter 时的 HasTables()，
// 那里表缺失会返回 ErrTablesMissing 让启动 Fatal（契约 §3.4）。
func (d *DBSlots) WarmUp() error {
	var err error
	d.once.Do(func() {
		if d.db == nil {
			return
		}
		if !d.db.Migrator().HasTable(&models.TelemetrySlot{}) {
			return
		}
		var rows []models.TelemetrySlot
		if e := d.db.Order("slot_id ASC").Find(&rows).Error; e != nil {
			err = fmt.Errorf("telemetry: warm up slots: %w", e)
			return
		}
		d.mem.mu.Lock()
		defer d.mem.mu.Unlock()
		for i := range rows {
			fields, ferr := UnmarshalFields(rows[i].FieldsJSON)
			if ferr != nil {
				// 单行坏掉不该让整个字典失效：跳过并继续（该 slot 的帧解码时
				// 会因为"未知 slot"被跳过并打点，是可见的失败而非静默错解）。
				continue
			}
			s := &Slot{
				ID:                rows[i].SlotID,
				Signature:         rows[i].Signature,
				CommandTemplateID: rows[i].CommandTemplateID,
				SpecVersion:       uint16(rows[i].SpecVersion),
				Fields:            fields,
			}
			if _, dup := d.mem.bySig[s.Signature]; dup {
				continue
			}
			d.mem.bySig[s.Signature] = s
			d.mem.byID[s.ID] = s
			if s.ID >= d.mem.nextID {
				d.mem.nextID = s.ID + 1
			}
		}
	})
	return err
}

// persist 把新 slot 写进 telemetry_slot 并返回分配到的 ID。
//
// 并发安全靠**唯一索引**：两个进程/协程同时首次见到同一签名时，只有一个
// INSERT 成功，另一个读回已有行。这里用 ON CONFLICT DO NOTHING + 回查，
// 而不是 SELECT-then-INSERT（后者在并发下必丢一次）。
func (d *DBSlots) persist(s *Slot) (int16, error) {
	if d.db == nil {
		return 0, fmt.Errorf("telemetry: no db for slot persistence")
	}
	fieldsJSON, err := s.MarshalFields()
	if err != nil {
		return 0, err
	}
	row := models.TelemetrySlot{
		Signature:         s.Signature,
		CommandTemplateID: s.CommandTemplateID,
		SpecVersion:       int16(s.SpecVersion),
		FieldCount:        int16(len(s.Fields)),
		FieldsJSON:        fieldsJSON,
	}
	// ON CONFLICT DO NOTHING + 回查：并发「首次见到同一签名」时只有一个 INSERT
	// 生效，另一个读回已存在的行。SELECT-then-INSERT 在并发下必丢一次（唯一索引
	// 会把它变成错误，而错误又会让那一帧的 slot 解析失败）。
	res := d.db.Session(&gorm.Session{}).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&row)
	if res.Error != nil {
		return 0, fmt.Errorf("telemetry: insert slot: %w", res.Error)
	}
	if res.RowsAffected > 0 && row.SlotID > 0 {
		return row.SlotID, nil
	}
	var existing models.TelemetrySlot
	if err := d.db.Session(&gorm.Session{}).
		Where("signature = ?", s.Signature).
		First(&existing).Error; err != nil {
		return 0, fmt.Errorf("telemetry: read back slot %s: %w", s.Signature, err)
	}
	return existing.SlotID, nil
}

// Resolve 实现 SlotDictionary。
func (d *DBSlots) Resolve(commandTemplateID int64, fieldNames []string) (*Slot, error) {
	if err := d.WarmUp(); err != nil {
		return nil, err
	}
	slot, err := d.mem.Resolve(commandTemplateID, fieldNames)
	if err != nil {
		return nil, err
	}
	d.observeUnknownFields(slot)
	return slot, nil
}

// ByID 实现 SlotDictionary。
func (d *DBSlots) ByID(slotID int16) (*Slot, bool) {
	if err := d.WarmUp(); err != nil {
		return nil, false
	}
	return d.mem.ByID(slotID)
}

// All 实现 SlotDictionary。
func (d *DBSlots) All() []*Slot {
	if err := d.WarmUp(); err != nil {
		return nil
	}
	return d.mem.All()
}

// UnknownFields 返回解析到未登记字段（走 FallbackSpec）的累计次数。
func (d *DBSlots) UnknownFields() int64 {
	d.unknownMu.Lock()
	defer d.unknownMu.Unlock()
	return d.unknownFields
}

func (d *DBSlots) observeUnknownFields(s *Slot) {
	n := int64(0)
	for _, f := range s.Fields {
		if _, known := SpecFor(f.Name); !known {
			n++
		}
	}
	if n == 0 {
		return
	}
	d.unknownMu.Lock()
	d.unknownFields += n
	d.unknownMu.Unlock()
}
