package geo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func withProviders(t *testing.T, urls ...string) {
	t.Helper()
	old := ipProviderEndpoints
	ipProviderEndpoints = urls
	resetIPCacheForTest()
	t.Cleanup(func() {
		ipProviderEndpoints = old
		resetIPCacheForTest()
	})
}

func TestResolveIPParsesProviderShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"ipwho", `{"success":true,"latitude":37.3393947,"longitude":-121.8949575,"city":"San Jose","region":"California","country":"United States","timezone":{"id":"America/Los_Angeles"}}`},
		{"ipapi", `{"latitude":37.33,"longitude":-121.89,"city":"Santa Clara","region":"California","country_name":"United States","timezone":"America/Los_Angeles"}`},
		{"ipinfo", `{"city":"San Jose","region":"California","country":"US","loc":"37.3394,-121.8950","timezone":"America/Los_Angeles"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			withProviders(t, srv.URL)
			loc, err := ResolveIP(context.Background())
			if err != nil {
				t.Fatalf("ResolveIP: %v", err)
			}
			if loc.Timezone != "America/Los_Angeles" || loc.Source != "ip" {
				t.Fatalf("loc = %+v", loc)
			}
			if loc.Latitude < 37 || loc.Latitude > 38 {
				t.Fatalf("lat = %v", loc.Latitude)
			}
		})
	}
}

func TestResolveIPFallsBackAndCaches(t *testing.T) {
	var calls atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"success":true,"latitude":31.22,"longitude":121.46,"timezone":{"id":"Asia/Shanghai"}}`)
	}))
	defer good.Close()

	withProviders(t, bad.URL, good.URL)
	loc, err := ResolveIP(context.Background())
	if err != nil || loc.Timezone != "Asia/Shanghai" {
		t.Fatalf("loc=%+v err=%v", loc, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2（第一个失败后应降级到第二个）", calls.Load())
	}
	// 缓存命中：不应再打提供方。
	if _, err := ResolveIP(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("缓存未命中：calls = %d", calls.Load())
	}
}

func TestResolveIPRejectsInvalidTimezoneAndCoords(t *testing.T) {
	badTZ := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"latitude":31.2,"longitude":121.4,"timezone":"Not/AZone"}`)
	}))
	defer badTZ.Close()
	withProviders(t, badTZ.URL)
	if _, err := ResolveIP(context.Background()); err == nil {
		t.Fatal("非法时区必须被拒绝（否则日出计算会在运行期失败）")
	}

	badCoord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"latitude":999,"longitude":0,"timezone":"Asia/Shanghai"}`)
	}))
	defer badCoord.Close()
	withProviders(t, badCoord.URL)
	if _, err := ResolveIP(context.Background()); err == nil {
		t.Fatal("越界坐标必须被拒绝")
	}
}

func TestResolveIPAllProvidersFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer bad.Close()
	withProviders(t, bad.URL)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := ResolveIP(ctx); err == nil {
		t.Fatal("全部失败时应返回错误（前端据此提示改用手动城市）")
	}
}
