// Package geo 提供日出日落规则所需的**地理位置来源**（2026-11-14）。
//
// 用户裁决：规则的纬度/经度/时区只有两个来源 ——
//
//  1. 外网 IP 定位（服务端发起，见 ip.go）；
//  2. 用户在界面上手动选择"设备所在城市"（本文件的城市目录）。
//
// 时区**统一由地理位置带出**（IP 服务返回 timezone；城市目录每个城市自带 IANA 时区），
// 界面不再提供时区输入框。这样做的原因：时区是地理位置的函数，让用户单独设置
// 只会产生"坐标在上海、时区填成 UTC"这种物理上矛盾、且会静默错开日出日落的组合。
//
// 城市目录来源：GeoNames cities15000（CC BY 4.0），筛选规则与生成脚本见
// cities.json 同目录的 README（若不存在则以本注释为准）：
//   · 中国（含港澳台）人口 ≥ 3 万或地级行政区驻地；
//   · 其它国家人口 ≥ 50 万或首都；
//   · 名称优先取该城市的中文别名（Han 字符），否则用 GeoNames 主名；
//   · 时区直接取 GeoNames 的 IANA 字段，加载失败的条目在启动时丢弃。
package geo

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed cities.json
var citiesJSON []byte

// City 是城市目录的一条记录（JSON 字段即 API 返回字段）。
type City struct {
	Name        string  `json:"name"`         // 展示名（中国城市优先中文）
	Pinyin      string  `json:"pinyin"`       // ASCII 名，供拼音/英文搜索
	Country     string  `json:"country"`      // 国家展示名（尽量中文）
	CountryCode string  `json:"country_code"` // ISO-3166 alpha-2
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Timezone    string  `json:"timezone"` // IANA 名，由地理数据自带
}

type cityFile struct {
	Cities []City `json:"cities"`
}

var (
	citiesOnce sync.Once
	cities     []City
	citiesErr  error
)

// Cities 返回目录全量（只读，调用方不得修改）。
func Cities() ([]City, error) {
	citiesOnce.Do(func() {
		var f cityFile
		if err := json.Unmarshal(citiesJSON, &f); err != nil {
			citiesErr = fmt.Errorf("geo: 解析内置城市目录失败: %w", err)
			return
		}
		// 启动期剔除时区非法的条目：宁可少一个城市，也不要让一条坏数据
		// 在保存规则时通过、在计算日出时静默失败。
		valid := make([]City, 0, len(f.Cities))
		for _, c := range f.Cities {
			if c.Name == "" || !ValidCoordinates(c.Latitude, c.Longitude) || !ValidTimezone(c.Timezone) {
				continue
			}
			valid = append(valid, c)
		}
		cities = valid
	})
	return cities, citiesErr
}

// ValidCoordinates 是坐标范围判据（与 handler 的 fail-closed 校验同源）。
func ValidCoordinates(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// ValidTimezone 判断 IANA 时区能否被 Go 加载。
func ValidTimezone(tz string) bool {
	if strings.TrimSpace(tz) == "" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// SearchCities 按名称/拼音做前缀或包含匹配，按相关度 + 目录顺序（人口降序）返回。
//
// 目录本身按人口降序排列（生成时保证），因此同分时用 sort.SliceStable 即得到
// "大城市优先"——这正符合"设备城市"的选择习惯（用户先看到省会/大城市）。
func SearchCities(query string, limit int) ([]City, error) {
	all, err := Cities()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		if len(all) > limit {
			return all[:limit], nil
		}
		return all, nil
	}

	type scored struct {
		city  City
		score int
	}
	hits := make([]scored, 0, 16)
	for _, c := range all {
		name := strings.ToLower(c.Name)
		pinyin := strings.ToLower(c.Pinyin)
		score := 0
		switch {
		case name == q || pinyin == q:
			score = 100
		case strings.HasPrefix(name, q) || strings.HasPrefix(pinyin, q):
			score = 80
		case strings.Contains(name, q) || strings.Contains(pinyin, q):
			score = 60
		}
		if score > 0 {
			hits = append(hits, scored{city: c, score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]City, len(hits))
	for i, h := range hits {
		out[i] = h.city
	}
	return out, nil
}
