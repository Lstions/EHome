package geo

// ip.go — 外网 IP 定位（2026-11-14）。
//
// 规则坐标来源之一："自动定位"必须看到**设备所在网络**的出口位置，所以由服务端
// 发起请求（浏览器端拿到的是用户自己电脑的出口 IP，可能和部署机相同，但把第三方
// 请求放到浏览器会引入 CORS/隐私/被墙三类问题；服务端一次缓存 10 分钟即可）。
//
// 多提供方顺序降级：ipwho.is → ipapi.co → ipinfo.io。它们都返回经纬度 + IANA
// 时区（时区随位置一起给，前端不再让用户填）。任何一个成功即返回；全部失败时报
// 一条聚合错误，前端提示"自动定位不可用，请手动选择城市"。
//
// 网络边界：
//   - 5s 超时，响应体限制 64KB（防止被投毒的 DNS/代理返回超大响应打爆内存）；
//   - 默认 http.Client 使用 http.ProxyFromEnvironment，因此部署机设置了
//     HTTPS_PROXY/HTTP_PROXY 时自动走代理（本仓网络受限环境的既定做法）；
//   - 结果缓存 10 分钟：规则表单反复打开不应反复打第三方接口（免费额度有限）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// IPLocation 是 IP 定位结果（JSON 即 /system/geo/ip 的 data）。
type IPLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
	City      string  `json:"city,omitempty"`
	Region    string  `json:"region,omitempty"`
	Country   string  `json:"country,omitempty"`
	Source    string  `json:"source"` // 恒为 "ip"，供前端回填 location_source
}

var (
	// 变量而不是常量：单测用 httptest 服务器替换，生产不受影响。
	ipProviderEndpoints = []string{
		"https://ipwho.is/",
		"https://ipapi.co/json/",
		"https://ipinfo.io/json",
	}
	ipHTTPClient = &http.Client{Timeout: 5 * time.Second}
	ipCacheTTL   = 10 * time.Minute

	ipCacheMu  sync.Mutex
	ipCached   *IPLocation
	ipCachedAt time.Time
)

// ResolveIP 返回部署机出口 IP 的地理位置（带 10 分钟缓存）。
func ResolveIP(ctx context.Context) (IPLocation, error) {
	ipCacheMu.Lock()
	if ipCached != nil && time.Since(ipCachedAt) < ipCacheTTL {
		loc := *ipCached
		ipCacheMu.Unlock()
		return loc, nil
	}
	ipCacheMu.Unlock()

	var errs []string
	for _, endpoint := range ipProviderEndpoints {
		loc, err := resolveIPFrom(ctx, endpoint)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}
		ipCacheMu.Lock()
		ipCached = &loc
		ipCachedAt = time.Now()
		ipCacheMu.Unlock()
		return loc, nil
	}
	if len(errs) == 0 {
		return IPLocation{}, fmt.Errorf("geo: 未配置 IP 定位服务")
	}
	return IPLocation{}, fmt.Errorf("geo: 所有 IP 定位服务均失败: %s", strings.Join(errs, "; "))
}

// resetIPCacheForTest 清缓存（仅同包测试使用）。
func resetIPCacheForTest() {
	ipCacheMu.Lock()
	ipCached = nil
	ipCachedAt = time.Time{}
	ipCacheMu.Unlock()
}

func resolveIPFrom(ctx context.Context, endpoint string) (IPLocation, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return IPLocation{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ehome-geo/1.0")
	resp, err := ipHTTPClient.Do(req)
	if err != nil {
		return IPLocation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IPLocation{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return IPLocation{}, err
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return IPLocation{}, fmt.Errorf("响应不是 JSON: %w", err)
	}
	if v, ok := raw["success"].(bool); ok && !v {
		return IPLocation{}, fmt.Errorf("provider success=false")
	}
	if v, ok := raw["bogon"].(bool); ok && v {
		return IPLocation{}, fmt.Errorf("provider 判定为内网/保留地址")
	}

	lat, okLat := numberField(raw["latitude"])
	lon, okLon := numberField(raw["longitude"])
	if (!okLat || !okLon) && raw["loc"] != nil {
		// ipinfo.io 用 "loc": "37.3394,-121.8950"
		if s, ok := raw["loc"].(string); ok {
			parts := strings.Split(s, ",")
			if len(parts) == 2 {
				lat, okLat = parseFloat(parts[0])
				lon, okLon = parseFloat(parts[1])
			}
		}
	}
	if !okLat || !okLon {
		return IPLocation{}, fmt.Errorf("响应缺少经纬度")
	}
	loc := IPLocation{
		Latitude:  lat,
		Longitude: lon,
		City:      stringField(raw["city"]),
		Region:    firstNonEmpty(stringField(raw["region"]), stringField(raw["regionName"])),
		Country:   firstNonEmpty(stringField(raw["country_name"]), stringField(raw["country"])),
		Timezone:  timezoneField(raw["timezone"]),
		Source:    "ip",
	}
	if !ValidCoordinates(loc.Latitude, loc.Longitude) {
		return IPLocation{}, fmt.Errorf("经纬度越界: %v,%v", loc.Latitude, loc.Longitude)
	}
	if !ValidTimezone(loc.Timezone) {
		return IPLocation{}, fmt.Errorf("时区不可用: %q", loc.Timezone)
	}
	return loc, nil
}

func numberField(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		return parseFloat(n)
	}
	return 0, false
}

func parseFloat(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil
}

func stringField(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// timezoneField 兼容两种形状：字符串（ipapi/ipinfo）与对象 {"id": "Asia/Shanghai"}（ipwho.is）。
func timezoneField(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		return stringField(t["id"])
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
