package sensorfield

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// virtualFnSpec 是派生函数集的静态规格（方案 §6.2“函数集，唯一一份”）。
type virtualFnSpec struct {
	Name      string
	MinArgs   int
	MaxArgs   int  // 0 = 不限制
	FamilyArg bool // max_of/min_of/sum_of：参数必须是 family selector 且不带聚合后缀
	HasScale  bool // scale：a × Scale
}

// virtualFnSpecs 是唯一函数表；实现与校验共用，禁止两处各写一份。
var virtualFnSpecs = []virtualFnSpec{
	{Name: "product", MinArgs: 2},
	{Name: "sum", MinArgs: 2},
	{Name: "diff", MinArgs: 2},
	{Name: "abs_diff", MinArgs: 2, MaxArgs: 2},
	{Name: "ratio", MinArgs: 2, MaxArgs: 2},
	{Name: "scale", MinArgs: 1, MaxArgs: 1, HasScale: true},
	{Name: "sum_of", MinArgs: 1, MaxArgs: 1, FamilyArg: true},
	{Name: "max_of", MinArgs: 1, MaxArgs: 1, FamilyArg: true},
	{Name: "min_of", MinArgs: 1, MaxArgs: 1, FamilyArg: true},
}

// VirtualFnInfo 是 /schema 端点用的派生函数描述（方案 §8.4 的 virtual_fns 形状）。
type VirtualFnInfo struct {
	Name      string `json:"name"`
	MinArgs   int    `json:"min_args"`
	MaxArgs   int    `json:"max_args,omitempty"`
	FamilyArg bool   `json:"arg_family,omitempty"`
	HasScale  bool   `json:"has_scale,omitempty"`
}

// VirtualFns 返回派生函数白名单（避免前端/后端两套词表漂移）。
func VirtualFns() []VirtualFnInfo {
	out := make([]VirtualFnInfo, 0, len(virtualFnSpecs))
	for _, s := range virtualFnSpecs {
		out = append(out, VirtualFnInfo{Name: s.Name, MinArgs: s.MinArgs, MaxArgs: s.MaxArgs, FamilyArg: s.FamilyArg, HasScale: s.HasScale})
	}
	return out
}

// virtualFnSpecByName 按名字查函数规格。
func virtualFnSpecByName(name string) (virtualFnSpec, bool) {
	for _, s := range virtualFnSpecs {
		if s.Name == name {
			return s, true
		}
	}
	return virtualFnSpec{}, false
}

// virtualArgRef 是派生函数的一个标量参数（已解析的 selector）。
type virtualArgRef struct {
	kind     Kind
	literal  string // KindLiteral
	semantic string // KindSemantic
	virtual  string // KindVirtual
}

// virtualNode 是一个派生的运行期定义（设备无关，存于 snapshot；不可变）。
type virtualNode struct {
	name    string
	fn      string
	spec    virtualFnSpec
	scale   float64
	scalars []virtualArgRef // 标量参数（product/sum/diff/abs_diff/ratio/scale）
	family  string          // *_of 的族前缀
	depth   int             // 嵌套深度（1 = 不依赖其它派生）
}

// boundVirtual 是绑定到具体设备后的派生运行树（Compile 产物的一部分，不可变）。
type boundVirtual struct {
	node    *virtualNode
	scalars []boundVirtualArg
	family  *boundVirtualFamily
}

// boundVirtualArg 是绑定后的标量参数。
type boundVirtualArg struct {
	kind        Kind
	literalName string
	physical    string // semantic 绑定到的物理名
	bound       bool   // semantic 是否成功绑定（false ⇒ 求值 unbound）
	numeric     bool   // 已知类型是否参与数值比较（未知类型按数值处理）
	nested      *boundVirtual
}

// boundVirtualFamily 是 *_of 绑定后的族信息（缺员判定用已知集合）。
type boundVirtualFamily struct {
	prefix     string
	known      []string
	knownSet   map[string]struct{}
	nonNumeric map[string]struct{}
	truncated  bool
}

// buildVirtuals 校验派生定义集合并构建不可变运行节点（方案 §6.2）。
//
// aliases 非空且已配置语义时校验参数里的 @semantic 是否存在；
// 若别名为空（冷启动顺序未定）则留给 Registry.Validate 复核。
func buildVirtuals(defs []VirtualDef, cfg Config, aliases *aliasTable) (map[string]*virtualNode, error) {
	cfg = defaultConfig(cfg)
	nodes := make(map[string]*virtualNode, len(defs))
	order := make([]string, 0, len(defs))

	// 第一遍：名字/函数/参数个数与类型。
	for i, def := range defs {
		name := strings.TrimSpace(def.Name)
		if !isPhysicalName(name) {
			return nil, fmt.Errorf("%w: virtual #%d has invalid name %q", ErrVirtualInvalid, i, def.Name)
		}
		if _, dup := nodes[name]; dup {
			return nil, fmt.Errorf("%w: duplicate virtual name %q", ErrVirtualInvalid, name)
		}
		spec, ok := virtualFnSpecByName(def.Fn)
		if !ok {
			return nil, fmt.Errorf("%w: virtual %q has unknown fn %q", ErrVirtualInvalid, name, def.Fn)
		}
		if len(def.Args) < spec.MinArgs || (spec.MaxArgs > 0 && len(def.Args) > spec.MaxArgs) {
			return nil, fmt.Errorf("%w: virtual %q fn %q expects %d..%d args, got %d", ErrVirtualInvalid, name, spec.Name, spec.MinArgs, spec.MaxArgs, len(def.Args))
		}
		node := &virtualNode{name: name, fn: spec.Name, spec: spec, scale: def.Scale}
		for _, rawArg := range def.Args {
			ps, err := parseSelector(rawArg, cfg)
			if err != nil {
				return nil, fmt.Errorf("%w: virtual %q arg %q: %v", ErrVirtualInvalid, name, rawArg, err)
			}
			if spec.FamilyArg {
				if ps.kind != KindFamily {
					return nil, fmt.Errorf("%w: virtual %q fn %q arg %q must be a family selector", ErrVirtualInvalid, name, spec.Name, ps.raw)
				}
				if ps.aggExplicit {
					return nil, fmt.Errorf("%w: virtual %q fn %q arg %q must not carry an aggregate suffix", ErrVirtualInvalid, name, spec.Name, ps.raw)
				}
				node.family = ps.prefix
				continue
			}
			if ps.kind == KindFamily {
				return nil, fmt.Errorf("%w: virtual %q fn %q arg %q: family selector only allowed for *_of functions", ErrVirtualInvalid, name, spec.Name, ps.raw)
			}
			ref := virtualArgRef{kind: ps.kind}
			switch ps.kind {
			case KindLiteral:
				ref.literal = ps.literal
			case KindSemantic:
				ref.semantic = ps.semantic
				if aliases != nil && len(aliases.order) > 0 && aliases.lookup(ref.semantic) == nil {
					return nil, fmt.Errorf("%w: virtual %q references unknown semantic @%s", ErrUnknownSemantic, name, ref.semantic)
				}
			case KindVirtual:
				ref.virtual = ps.virtual
			}
			node.scalars = append(node.scalars, ref)
		}
		if spec.HasScale {
			if math.IsNaN(def.Scale) || math.IsInf(def.Scale, 0) {
				return nil, fmt.Errorf("%w: virtual %q scale factor is not finite", ErrVirtualInvalid, name)
			}
			if def.Scale == 0 {
				// 0 与“未配置”不可区分，按配置错误 fail-closed（不猜测用户想要 ×0）。
				return nil, fmt.Errorf("%w: virtual %q fn scale requires a non-zero scale factor", ErrVirtualInvalid, name)
			}
		}
		nodes[name] = node
		order = append(order, name)
	}

	// 第二遍：引用存在性 + 环检测 + 深度（拓扑序由 DFS 完成）。
	sort.Strings(order)
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(nodes))
	var visit func(name string) error
	visit = func(name string) error {
		switch color[name] {
		case grey:
			return fmt.Errorf("%w: %q", ErrVirtualCycle, name)
		case black:
			return nil
		}
		color[name] = grey
		node := nodes[name]
		maxDep := 0
		for _, arg := range node.scalars {
			if arg.kind != KindVirtual {
				continue
			}
			dep, ok := nodes[arg.virtual]
			if !ok {
				return fmt.Errorf("%w: virtual %q references unknown virtual %q", ErrVirtualInvalid, name, arg.virtual)
			}
			if err := visit(arg.virtual); err != nil {
				return err
			}
			if dep.depth > maxDep {
				maxDep = dep.depth
			}
		}
		node.depth = maxDep + 1
		if node.depth > cfg.MaxVirtualDepth {
			return fmt.Errorf("%w: virtual %q depth %d exceeds max %d", ErrVirtualInvalid, name, node.depth, cfg.MaxVirtualDepth)
		}
		color[name] = black
		return nil
	}
	for _, name := range order {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}
