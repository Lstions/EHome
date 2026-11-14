package geo

import (
	"strings"
	"testing"
)

func TestCatalogHasMajorCitiesAndValidData(t *testing.T) {
	all, err := Cities()
	if err != nil {
		t.Fatalf("Cities(): %v", err)
	}
	if len(all) < 1500 {
		t.Fatalf("城市目录只有 %d 条，明显少于预期（内置数据缺失？）", len(all))
	}
	for _, want := range []string{"北京", "上海", "深圳", "成都", "乌鲁木齐", "香港", "台北"} {
		found := false
		for _, c := range all {
			if c.Name == want {
				found = true
				if !ValidCoordinates(c.Latitude, c.Longitude) {
					t.Fatalf("%s 坐标越界: %v,%v", want, c.Latitude, c.Longitude)
				}
				if !ValidTimezone(c.Timezone) {
					t.Fatalf("%s 时区不可用: %q", want, c.Timezone)
				}
				break
			}
		}
		if !found {
			t.Fatalf("目录缺少 %s（生成脚本筛选逻辑退化？）", want)
		}
	}
}

func TestSearchCities(t *testing.T) {
	cases := []struct{ query, want string }{
		{"北京", "北京"},
		{"上海", "上海"},
		{"bei", "北京"},   // 拼音前缀
		{"shenzhen", "深圳"},
		{"乌鲁木齐", "乌鲁木齐"},
		{"东京", "东京"},
	}
	for _, tc := range cases {
		hits, err := SearchCities(tc.query, 5)
		if err != nil {
			t.Fatalf("SearchCities(%q): %v", tc.query, err)
		}
		if len(hits) == 0 {
			t.Fatalf("SearchCities(%q) 无结果", tc.query)
		}
		if hits[0].Name != tc.want {
			t.Fatalf("SearchCities(%q)[0] = %q, want %q", tc.query, hits[0].Name, tc.want)
		}
	}
}

func TestSearchCitiesEmptyQueryAndLimit(t *testing.T) {
	hits, err := SearchCities("  ", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("空查询 limit=3 返回 %d 条", len(hits))
	}
	// 目录按人口降序：第一条应是人口最大的城市之一（上海/北京）。
	if !strings.Contains("上海北京重庆", hits[0].Name) {
		t.Fatalf("空查询首条 = %q，不符合人口降序", hits[0].Name)
	}
	// limit 越界收敛为 20，不报错。
	hits, err = SearchCities("", -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 20 {
		t.Fatalf("limit=-1 应收敛为 20，实际 %d", len(hits))
	}
}
