package database

import (
	"os"
	"strings"
	"testing"
)

// 生产 AutoMigrate 路径覆盖性: 新增模型/字段必须在 database/gorm.go 的
// AutoMigrate(...) 实参列表里 —— testutil 的模型清单与生产清单是两份,
// 只在 testutil 里加会让测试库有列而生产库没有 (迁移静默缺失)。
func TestPreconditionModelsAreInProductionAutoMigrate(t *testing.T) {
	raw, err := os.ReadFile("gorm.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "DB.AutoMigrate(")
	if start < 0 {
		t.Fatal("找不到 AutoMigrate 调用")
	}
	end := strings.Index(src[start:], "\n\t);")
	if end < 0 {
		t.Fatal("找不到 AutoMigrate 实参列表结尾")
	}
	args := src[start : start+end]
	for _, want := range []string{"&models.AutomationRule{}", "&models.CommandMetricsBaseline{}"} {
		if !strings.Contains(args, want) {
			t.Fatalf("生产 AutoMigrate 列表缺少 %s —— 测试库会有它而生产库不会 (静默迁移缺失)", want)
		}
	}
}
