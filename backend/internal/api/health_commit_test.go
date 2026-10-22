package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestHealthEchoesBuildCommit 钉住 /health 回显构建 commit。
//
// 为什么需要它（2026-10-21 生产事故）：那次部署的镜像"不是从 main 构建"，
// 生产回退了 91 分钟的解析器语义，事后只能靠 strings 推断镜像来源。
// 把 commit 烙进二进制并经 /health 暴露后，"部署的是哪份代码"变成可查询事实。
// 若有人把这个字段删掉，运维会重新失去唯一的运行时来源凭据 —— 故用测试锁住。
func TestHealthEchoesBuildCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := apiBuildCommit
	SetBuildCommit("deadbeefcafe")
	defer SetBuildCommit(prev)

	r := gin.New()
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "commit": apiBuildCommit})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status=%q want ok", body["status"])
	}
	if body["commit"] != "deadbeefcafe" {
		t.Errorf("commit=%q want deadbeefcafe（构建来源必须可查询）", body["commit"])
	}
}

// TestSetBuildCommitIgnoresEmpty 空串不得覆盖已有值（防止未注入时把来源擦成空）。
func TestSetBuildCommitIgnoresEmpty(t *testing.T) {
	prev := apiBuildCommit
	defer SetBuildCommit(prev)
	SetBuildCommit("abc123")
	SetBuildCommit("")
	if apiBuildCommit != "abc123" {
		t.Errorf("空串覆盖了已知 commit: %q", apiBuildCommit)
	}
}
