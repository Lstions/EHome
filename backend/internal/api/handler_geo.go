package api

// handler_geo.go — 坐标来源解析（2026-11-14）。
//
// 日出日落规则的纬度/经度/时区只有两个来源：
//
//	GET /api/v1/system/geo/ip     外网 IP 定位（服务端发起，见 internal/geo/ip.go）
//	GET /api/v1/system/geo/cities 手动选择"设备城市"（内置 GeoNames 目录）
//
// 时区随位置一起返回，**前端不再提供时区输入框**（用户裁决：时区统一按地理位置
// 自动填充，不允许用户设置）。两个端点都在 v1 鉴权组内：规则表单只在登录后使用，
// 没必要把"这台机器的出口位置"暴露成匿名接口。

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"ehome/backend/internal/geo"

	"github.com/gin-gonic/gin"
)

// geoIPResolver 是 IP 定位的间接引用：单测注入固定结果，避免依赖外网。
// 生产恒为 geo.ResolveIP。
var geoIPResolver = geo.ResolveIP

func registerGeoRoutes(v1 *gin.RouterGroup) {
	v1.GET("/system/geo/ip", getGeoIPLocation)
	v1.GET("/system/geo/cities", getGeoCities)
}

func getGeoIPLocation(c *gin.Context) {
	// 比 geo 内部的单提供方超时略长：三个提供方顺序降级，最坏 3×5s；
	// 这里 8s 让"前两个挂、第三个成功"仍有机会返回，同时不让请求无限挂住。
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	loc, err := geoIPResolver(ctx)
	if err != nil {
		// 503 而不是 500：这是"外部依赖不可用"的临时态，前端据此提示
		// "自动定位不可用，请手动选择城市"，而不是让用户以为规则页坏了。
		Error(c, http.StatusServiceUnavailable, "自动定位不可用，请改用手动选择城市: "+err.Error())
		return
	}
	Success(c, loc)
}

func getGeoCities(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	cities, err := geo.SearchCities(c.Query("q"), limit)
	if err != nil {
		Error(c, http.StatusInternalServerError, "城市目录不可用")
		return
	}
	Success(c, gin.H{"items": cities, "total": len(cities)})
}
