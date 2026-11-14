package models

import (
	"errors"
	"testing"
)

func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

// 2026-11-13 用户裁决「不需要节点位置」：日出/日落规则的坐标与时区**只来自规则行**，
// 节点表不再参与解析。本文件按新语义重写：
//   - 三项齐全 ⇒ 成功（含 0,0 这种合法零值）；
//   - 缺任一项 ⇒ 明确错误（fail-closed），绝不回落到 UTC / 默认坐标；
//   - 节点列即使有值也不再影响结果（由 handler 层用例覆盖"节点有值但规则没填"）。

func TestResolveSunLocationRuleOnly(t *testing.T) {
	got, err := ResolveSunLocation(f64(-33.87), f64(151.21), str("Australia/Sydney"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Latitude != -33.87 || got.Longitude != 151.21 || got.Timezone != "Australia/Sydney" {
		t.Fatalf("解析结果错误: %+v", got)
	}
	if got.Location == nil || got.Location.String() != "Australia/Sydney" {
		t.Fatalf("Location 必须已解析: %+v", got.Location)
	}
}

// 0,0 是**合法坐标**（几内亚湾），必须被当成"已配置"而不是"缺失"。
// 这正是三列用指针而不是零值的原因。
func TestResolveSunLocationZeroCoordinatesAreValid(t *testing.T) {
	got, err := ResolveSunLocation(f64(0), f64(0), str("UTC"))
	if err != nil {
		t.Fatalf("0,0 是合法坐标, 不应报错: %v", err)
	}
	if got.Latitude != 0 || got.Longitude != 0 || got.Location == nil {
		t.Fatalf("值错误: %+v", got)
	}
}

// 时区名两端空白必须被裁剪（用户从别处复制粘贴常带空格）。
func TestResolveSunLocationTrimsTimezone(t *testing.T) {
	got, err := ResolveSunLocation(f64(1), f64(2), str("  Asia/Shanghai  "))
	if err != nil {
		t.Fatal(err)
	}
	if got.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone 未裁剪: %q", got.Timezone)
	}
}

// 缺任一配置都必须**报错**（O-7 fail-closed），绝不静默回落到默认坐标/UTC。
func TestResolveSunLocationFailClosed(t *testing.T) {
	cases := []struct {
		name string
		lat  *float64
		lon  *float64
		tz   *string
		want error
	}{
		{"全缺", nil, nil, nil, ErrSunLatitudeMissing},
		{"缺纬度", nil, f64(2), str("UTC"), ErrSunLatitudeMissing},
		{"缺经度", f64(1), nil, str("UTC"), ErrSunLongitudeMissing},
		{"缺时区", f64(1), f64(2), nil, ErrSunTimezoneMissing},
		{"时区空串", f64(1), f64(2), str(""), ErrSunTimezoneMissing},
		{"时区全空白", f64(1), f64(2), str("  "), ErrSunTimezoneMissing},
		{"纬度越界", f64(90.1), f64(2), str("UTC"), ErrSunLatitudeRange},
		{"纬度负越界", f64(-90.1), f64(2), str("UTC"), ErrSunLatitudeRange},
		{"经度越界", f64(1), f64(180.1), str("UTC"), ErrSunLongitudeRange},
		{"经度负越界", f64(1), f64(-180.1), str("UTC"), ErrSunLongitudeRange},
		{"时区名非法", f64(1), f64(2), str("Mars/Olympus"), ErrSunTimezoneInvalid},
		{"时区名拼错", f64(1), f64(2), str("Asia/Shanghi"), ErrSunTimezoneInvalid},
	}
	for _, c := range cases {
		got, err := ResolveSunLocation(c.lat, c.lon, c.tz)
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

// 反向断言：明确证明失败路径不会"顺手"给一个可用时区
// （选型 §6.3：回落 UTC 会让窗口静默偏移 8 小时，是安全相关错误）。
func TestResolveSunLocationNeverFallsBackToUTC(t *testing.T) {
	if _, err := ResolveSunLocation(f64(1), f64(2), nil); err == nil {
		t.Fatal("缺时区必须报错 —— 绝不回落到 UTC")
	}
}

// ResolveRuleSunLocation 包装必须与直接调用一致（唯一实现的入口）。
func TestResolveRuleSunLocationUsesRuleFields(t *testing.T) {
	lat, lon, tz := 31.23, 121.47, "Asia/Shanghai"
	got, err := ResolveRuleSunLocation(AutomationRule{Latitude: &lat, Longitude: &lon, Timezone: &tz})
	if err != nil {
		t.Fatal(err)
	}
	if got.Latitude != 31.23 || got.Longitude != 121.47 || got.Timezone != "Asia/Shanghai" {
		t.Fatalf("结果错误: %+v", got)
	}
	if _, err := ResolveRuleSunLocation(AutomationRule{}); !errors.Is(err, ErrSunLatitudeMissing) {
		t.Fatalf("空规则必须报 ErrSunLatitudeMissing, got %v", err)
	}
}

func TestValidateSunCoordinatesBoundaries(t *testing.T) {
	for _, c := range []struct{ lat, lon float64 }{{90, 180}, {-90, -180}, {0, 0}, {66.5, 25}} {
		if err := ValidateSunCoordinates(c.lat, c.lon); err != nil {
			t.Errorf("(%v,%v) 应合法: %v", c.lat, c.lon, err)
		}
	}
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
