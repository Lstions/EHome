package models

import (
	"errors"
	"testing"
)

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

// TestResolveSunLocationPrecedence 钉死 O-3 的解析顺序:
// 规则值 → 节点值 → 缺失 fail-closed。逐项独立断言, 防止"部分字段走错层"。
func TestResolveSunLocationPrecedence(t *testing.T) {
	node := &Node{Latitude: f64(39.9), Longitude: f64(116.4), Timezone: str("Asia/Shanghai")}

	t.Run("全继承节点", func(t *testing.T) {
		got, src, err := ResolveSunLocation(nil, nil, nil, node)
		if err != nil {
			t.Fatal(err)
		}
		if got.Latitude != 39.9 || got.Longitude != 116.4 || got.Timezone != "Asia/Shanghai" {
			t.Fatalf("继承值错误: %+v", got)
		}
		if src.Latitude != "node" || src.Longitude != "node" || src.Timezone != "node" {
			t.Fatalf("来源标注错误: %+v", src)
		}
	})

	t.Run("规则级全量覆盖", func(t *testing.T) {
		got, src, err := ResolveSunLocation(f64(-33.87), f64(151.21), str("Australia/Sydney"), node)
		if err != nil {
			t.Fatal(err)
		}
		if got.Latitude != -33.87 || got.Longitude != 151.21 || got.Timezone != "Australia/Sydney" {
			t.Fatalf("覆盖值错误: %+v", got)
		}
		if src.Latitude != "rule" || src.Longitude != "rule" || src.Timezone != "rule" {
			t.Fatalf("来源标注错误: %+v", src)
		}
	})

	// 逐字段混合: 这是最容易写错的分支 (可能整体走规则或整体走节点)。
	t.Run("逐字段混合", func(t *testing.T) {
		got, src, err := ResolveSunLocation(f64(-33.87), nil, str("UTC"), node)
		if err != nil {
			t.Fatal(err)
		}
		if got.Latitude != -33.87 {
			t.Errorf("lat 应取规则值, got %v", got.Latitude)
		}
		if got.Longitude != 116.4 {
			t.Errorf("lon 应继承节点, got %v", got.Longitude)
		}
		if got.Timezone != "UTC" {
			t.Errorf("tz 应取规则值, got %v", got.Timezone)
		}
		if src.Latitude != "rule" || src.Longitude != "node" || src.Timezone != "rule" {
			t.Errorf("来源标注错误: %+v", src)
		}
	})

	// 赤道/本初子午线上的 0 是**合法坐标**, 必须被当成"已配置"而不是"缺失"。
	// 这正是三列用指针而不是零值的原因。
	t.Run("零值坐标合法", func(t *testing.T) {
		got, _, err := ResolveSunLocation(f64(0), f64(0), str("UTC"), nil)
		if err != nil {
			t.Fatalf("0,0 是合法坐标, 不应报错: %v", err)
		}
		if got.Latitude != 0 || got.Longitude != 0 {
			t.Fatalf("值错误: %+v", got)
		}
		if got.Location == nil {
			t.Fatal("Location 必须已解析")
		}
	})

	// 节点零值坐标同样合法。
	t.Run("节点零值坐标合法", func(t *testing.T) {
		n := &Node{Latitude: f64(0), Longitude: f64(0), Timezone: str("UTC")}
		if _, _, err := ResolveSunLocation(nil, nil, nil, n); err != nil {
			t.Fatalf("节点 0,0 合法: %v", err)
		}
	})
}

// TestResolveSunLocationFailClosed 钉死 O-7: 缺任一配置都必须**报错**,
// 绝不静默回落到默认坐标/UTC/Asia-Shanghai。
func TestResolveSunLocationFailClosed(t *testing.T) {
	full := &Node{Latitude: f64(1), Longitude: f64(2), Timezone: str("UTC")}
	cases := []struct {
		name string
		lat  *float64
		lon  *float64
		tz   *string
		node *Node
		want error
	}{
		{"无节点无规则", nil, nil, nil, nil, ErrSunLatitudeMissing},
		{"节点缺纬度", nil, nil, nil, &Node{Longitude: f64(2), Timezone: str("UTC")}, ErrSunLatitudeMissing},
		{"节点缺经度", nil, nil, nil, &Node{Latitude: f64(1), Timezone: str("UTC")}, ErrSunLongitudeMissing},
		{"节点缺时区", nil, nil, nil, &Node{Latitude: f64(1), Longitude: f64(2)}, ErrSunTimezoneMissing},
		{"规则时区空串且节点无时区", nil, nil, str("  "), full.detachTimezone(), ErrSunTimezoneMissing},
		{"纬度越界", f64(90.1), f64(2), str("UTC"), nil, ErrSunLatitudeRange},
		{"纬度负越界", f64(-90.1), f64(2), str("UTC"), nil, ErrSunLatitudeRange},
		{"经度越界", f64(1), f64(180.1), str("UTC"), nil, ErrSunLongitudeRange},
		{"经度负越界", f64(1), f64(-180.1), str("UTC"), nil, ErrSunLongitudeRange},
		{"节点纬度越界", nil, nil, nil, &Node{Latitude: f64(91), Longitude: f64(2), Timezone: str("UTC")}, ErrSunLatitudeRange},
		{"时区名非法", f64(1), f64(2), str("Mars/Olympus"), nil, ErrSunTimezoneInvalid},
		{"时区名拼错", f64(1), f64(2), str("Asia/Shanghi"), nil, ErrSunTimezoneInvalid},
		{"规则时区非法覆盖合法节点", f64(1), f64(2), str("Not/AZone"), full, ErrSunTimezoneInvalid},
	}
	for _, c := range cases {
		got, _, err := ResolveSunLocation(c.lat, c.lon, c.tz, c.node)
		if err == nil {
			t.Errorf("%s: 必须 fail-closed 报错, 却成功了 (%+v)", c.name, got)
			continue
		}
		if !errors.Is(err, c.want) {
			t.Errorf("%s: 错误不符, got %v want %v", c.name, err, c.want)
		}
		if got.Location != nil {
			t.Errorf("%s: 报错时不得返回已解析 Location", c.name)
		}
	}
}

// detachTimezone 返回一个去掉时区字段的节点副本 (复用 full 的坐标)。
func (n *Node) detachTimezone() *Node {
	cp := *n
	cp.Timezone = nil
	return &cp
}

// TestResolveSunLocationNeverFallsBackToUTC 是 O-7 的**反向断言**:
// 明确证明失败路径不会"顺手"给一个可用时区 (选型 §6.3: 回落 UTC 会让窗口
// 静默偏移 8 小时, 是安全相关错误)。
func TestResolveSunLocationNeverFallsBackToUTC(t *testing.T) {
	_, _, err := ResolveSunLocation(f64(1), f64(2), nil, &Node{Latitude: f64(1), Longitude: f64(2)})
	if err == nil {
		t.Fatal("缺时区必须报错 —— 绝不回落到 UTC")
	}
	_, _, err = ResolveSunLocation(nil, nil, nil, nil)
	if err == nil {
		t.Fatal("完全无配置必须报错")
	}
}

func TestValidateSunCoordinatesBoundaries(t *testing.T) {
	// 端点合法。
	for _, c := range []struct{ lat, lon float64 }{{90, 180}, {-90, -180}, {0, 0}, {66.5, 25}} {
		if err := ValidateSunCoordinates(c.lat, c.lon); err != nil {
			t.Errorf("(%v,%v) 应合法: %v", c.lat, c.lon, err)
		}
	}
	// 越界非法。
	for _, c := range []struct{ lat, lon float64 }{{90.000001, 0}, {0, 180.000001}, {-90.000001, 0}, {0, -180.000001}} {
		if err := ValidateSunCoordinates(c.lat, c.lon); err == nil {
			t.Errorf("(%v,%v) 应非法", c.lat, c.lon)
		}
	}
}

func TestValidateTimezoneAcceptsIANA(t *testing.T) {
	for _, name := range []string{"UTC", "Asia/Shanghai", "America/New_York", "Arctic/Longyearbyen", "Europe/Helsinki"} {
		if _, err := ValidateTimezone(name); err != nil {
			t.Errorf("%s 应可解析: %v", name, err)
		}
	}
	for _, name := range []string{"", "  ", "Shanghai", "UTC+8", "Mars/Base"} {
		if _, err := ValidateTimezone(name); err == nil {
			t.Errorf("%q 应被拒绝", name)
		}
	}
}
