package sensorfield

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// 默认值（方案 §11）。Config 的零值字段按此补齐；Enabled 零值语义是“关闭”，不设默认。
const (
	defaultMaxSelectorChars = 64
	defaultMaxFamilySize    = 64
	defaultMaxVirtualDepth  = 4
	defaultSnapshotTTL      = 10 * time.Minute

	// defaultPreviewMaxSamples 是 /preview 试算的默认采样上限（方案 §8.3/§11）。
	defaultPreviewMaxSamples = 5000
)

// maxCompileCacheEntries 是编译缓存条目上限（防无界增长；规则/选择器组合数远小于此）。
const maxCompileCacheEntries = 4096

// defaultConfig 把 Config 零值字段补成默认值（方案 §11）。
func defaultConfig(cfg Config) Config {
	if cfg.MaxSelectorChars <= 0 {
		cfg.MaxSelectorChars = defaultMaxSelectorChars
	}
	if cfg.MaxFamilySize <= 0 {
		cfg.MaxFamilySize = defaultMaxFamilySize
	}
	if cfg.MaxVirtualDepth <= 0 {
		cfg.MaxVirtualDepth = defaultMaxVirtualDepth
	}
	if cfg.SnapshotTTL <= 0 {
		cfg.SnapshotTTL = defaultSnapshotTTL
	}
	if cfg.PreviewMaxSamples <= 0 {
		cfg.PreviewMaxSamples = defaultPreviewMaxSamples
	}
	return cfg
}

// parsedSelector 是 selector 的纯文法解析结果，不携带任何设备/快照信息。
//
// 语法层四类互斥（方案 §5.1/§5.4）：
//
//	@name             semantic
//	virtual:name      virtual
//	prefix_*[:agg]    family
//	^[a-z][a-z0-9_]*$ literal
type parsedSelector struct {
	raw         string    // TrimSpace 之后的原始串（错误信息与回显用）
	kind        Kind      // 四类之一
	literal     string    // KindLiteral：物理名
	prefix      string    // KindFamily：不含 "_*" 的前缀
	aggregate   Aggregate // KindFamily：默认 AggAny
	aggExplicit bool      // 是否显式写了 ":agg"（派生 *_of 参数禁止显式聚合）
	semantic    string    // KindSemantic：不含 "@"
	virtual     string    // KindVirtual：不含 "virtual:"
}

// syntaxErr 构造一个可 errors.Is 到指定哨兵的语法错误。
func syntaxErr(sentinel error, selector, format string, args ...any) error {
	return fmt.Errorf("%w: selector %q: %s", sentinel, selector, fmt.Sprintf(format, args...))
}

// isLowerASCII 判断 b 是否为 a-z。
func isLowerASCII(b byte) bool { return b >= 'a' && b <= 'z' }

// isDigitASCII 判断 b 是否为 0-9。
func isDigitASCII(b byte) bool { return b >= '0' && b <= '9' }

// isPhysicalName 判断 s 是否匹配物理名文法 ^[a-z][a-z0-9_]*$（仅 ASCII，大小写敏感）。
func isPhysicalName(s string) bool {
	if s == "" {
		return false
	}
	if !isLowerASCII(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if isLowerASCII(c) || isDigitASCII(c) || c == '_' {
			continue
		}
		return false
	}
	return true
}

// isFamilyPrefix 判断 s 是否可作为族前缀：物理名文法且不以 "_" 结尾（方案 §5.1）。
func isFamilyPrefix(s string) bool {
	return isPhysicalName(s) && s[len(s)-1] != '_'
}

// isASCII 判断整串是否为 7-bit ASCII。
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// matchFamilyIndex 判断 name 是否属于 prefix 族（严格 ^prefix_[0-9]+$，方案 §2/§5.2），
// 返回数字后缀。后缀溢出（如 30 位数字）时饱和为 math.MaxUint64，仍算族成员。
func matchFamilyIndex(prefix, name string) (uint64, bool) {
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	pl := len(prefix)
	if len(name) <= pl+1 || name[pl] != '_' {
		return 0, false
	}
	digits := name[pl+1:]
	var idx uint64
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if !isDigitASCII(c) {
			return 0, false
		}
		d := uint64(c - '0')
		if idx > (^uint64(0)-d)/10 {
			idx = ^uint64(0) // 饱和，保证仍是族成员且排序靠后
			continue
		}
		idx = idx*10 + d
	}
	return idx, true
}

// parseAggregate 把 ":suffix" 解析为聚合。区分三种错误（方案 §5.1 边界 6/7）：
//   - 白名单内 ⇒ 成功
//   - 含 : 或空格等非字母数字下划线字符 ⇒ invalid_selector_syntax（多聚合/冒号后脏）
//   - 纯字母数字下划线但不在白名单 ⇒ unsupported_aggregate（含大小写错误 MAX/median）
func parseAggregate(selector, agg string) (Aggregate, error) {
	if ValidAggregate(Aggregate(agg)) {
		return Aggregate(agg), nil
	}
	for i := 0; i < len(agg); i++ {
		c := agg[i]
		if isLowerASCII(c) || (c >= 'A' && c <= 'Z') || isDigitASCII(c) || c == '_' {
			continue
		}
		return "", syntaxErr(ErrInvalidSyntax, selector, "aggregate %q contains illegal character", agg)
	}
	return "", syntaxErr(ErrUnsupportedAggregate, selector, "aggregate %q is not supported", agg)
}

// parseSelector 按方案 §5.1 文法解析 selector。
//
// 顺序（评审要求逐条定义，勿调换）：
//  1. TrimSpace 先行；trim 后为空 ⇒ ErrEmptySelector
//  2. 长度 ≤ MaxSelectorChars（按 Unicode 字符数，DB VARCHAR(64) 口径）⇒ ErrSelectorTooLong
//  3. 仅 ASCII；非 ASCII ⇒ ErrInvalidSyntax
//  4. 形态判定：@ / virtual: / "_*" / literal；大小写敏感，不做静默小写化
func parseSelector(selector string, cfg Config) (parsedSelector, error) {
	cfg = defaultConfig(cfg)
	s := strings.TrimSpace(selector)
	if s == "" {
		return parsedSelector{}, ErrEmptySelector
	}
	if utf8.RuneCountInString(s) > cfg.MaxSelectorChars {
		return parsedSelector{}, fmt.Errorf("%w: %d chars > %d", ErrSelectorTooLong, utf8.RuneCountInString(s), cfg.MaxSelectorChars)
	}
	if !isASCII(s) {
		return parsedSelector{}, syntaxErr(ErrInvalidSyntax, s, "non-ASCII character")
	}

	// semantic
	if s[0] == '@' {
		name := s[1:]
		if name == "" {
			return parsedSelector{}, syntaxErr(ErrInvalidSyntax, s, "empty semantic name")
		}
		if !isPhysicalName(name) {
			return parsedSelector{}, syntaxErr(ErrInvalidSyntax, s, "invalid semantic name %q", name)
		}
		return parsedSelector{raw: s, kind: KindSemantic, semantic: name}, nil
	}

	// virtual
	const virtualPrefix = "virtual:"
	if strings.HasPrefix(s, virtualPrefix) {
		name := s[len(virtualPrefix):]
		if name == "" {
			return parsedSelector{}, syntaxErr(ErrInvalidSyntax, s, "empty virtual name")
		}
		if !isPhysicalName(name) {
			return parsedSelector{}, syntaxErr(ErrInvalidSyntax, s, "invalid virtual name %q", name)
		}
		return parsedSelector{raw: s, kind: KindVirtual, virtual: name}, nil
	}

	// family: 形如 prefix_*[:agg]
	if i := strings.Index(s, "_*"); i >= 0 {
		prefix := s[:i]
		rest := s[i+2:]
		if !isFamilyPrefix(prefix) {
			return parsedSelector{}, syntaxErr(ErrInvalidSyntax, s, "invalid family prefix %q", prefix)
		}
		ps := parsedSelector{raw: s, kind: KindFamily, prefix: prefix, aggregate: AggAny}
		switch {
		case rest == "":
			return ps, nil
		case strings.HasPrefix(rest, ":"):
			agg := rest[1:]
			if agg == "" {
				return parsedSelector{}, syntaxErr(ErrInvalidSyntax, s, "empty aggregate after ':'")
			}
			parsed, err := parseAggregate(s, agg)
			if err != nil {
				return parsedSelector{}, err
			}
			ps.aggregate = parsed
			ps.aggExplicit = true
			return ps, nil
		default:
			return parsedSelector{}, syntaxErr(ErrInvalidSyntax, s, "invalid family suffix %q", rest)
		}
	}

	// literal
	if !isPhysicalName(s) {
		return parsedSelector{}, syntaxErr(ErrInvalidSyntax, s, "invalid literal physical name")
	}
	return parsedSelector{raw: s, kind: KindLiteral, literal: s}, nil
}

// Aggregates 返回受支持的聚合白名单（固定顺序，首元素即默认聚合）。
// 供 /schema 端点与保存期校验使用，避免前端/后端两套词表漂移（方案 §8.4）。
func Aggregates() []Aggregate {
	return []Aggregate{AggAny, AggAll, AggMin, AggMax, AggAvg, AggSum}
}

// DefaultAggregate 返回 family 选择器的默认聚合（方案 §5.3：any）。
func DefaultAggregate() Aggregate { return AggAny }
