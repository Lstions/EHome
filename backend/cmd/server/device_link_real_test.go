package main

// 真机联调靶场：让**真实设备**（本机 USB 接的 S3）用 mTLS 连上**真实后端**。
//
// ## 与既有对锚的区别（本文件存在的理由）
//
// device_e2e_firmware_test.go 的对锚用的是**固件源码编译出的宿主程序**
// （firmware_tcp_e2e_client）—— 证明"两端源码能对上"，但**不经过 ESP32 的
// WiFi 栈 / mbedTLS / NVS 证书读取 / 真实任务调度**。device_e2e_test.go 用 Go 造
// 假设备，同样不经过这些。⇒ **两者都不是真机**，本文件补的正是这一段。
//
// ## 为什么用 go test 而不是新写一个 cmd/ 二进制
//
// 真实装配需要 testutil.OpenTestDB（GORM + 迁移）、websocket.Hub、真实
// nodemgr.Manager。用 go test 可复用既有 helper，**不引入第二套装配** ——
// 第二套会漂移，而漂移的那套测的就不是生产了。
//
// ## 用法
//
//	EHOME_REAL_DEVICE=1 EHOME_REAL_PKI=/tmp/ev90/pki \
//	  go test -count=1 -v -timeout 300s -run TestRealDeviceLink ./cmd/server/
//
// 不设 EHOME_REAL_DEVICE 时 **Skip**（不静默通过）：它需要真硬件，不能进常规 CI。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ehome/backend/internal/downlink"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/transport"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/testutil"
)

// ⚠ realDeviceLegacy（MQTT 替身）已随 MQTT 一起删除（2026-10-08）。
//
// 它存在的意义是"若 HelloAck 走了 MQTT 而不是 TCP，这里会留下痕迹"。
// MQTT 已不存在 ⇒ 没有第二条可误走的通道；而"选路必须与 transport 共用
// **同一个** Registry（cfg.Registry）"这一条**仍然成立且更要紧**，因为现在
// 没有会话就是硬错误（downlink.Publish 返回 error），不再是静默误投。

func TestRealDeviceLink(t *testing.T) {
	if os.Getenv("EHOME_REAL_DEVICE") != "1" {
		t.Skip("EHOME_REAL_DEVICE!=1：本用例需要真设备在本机 USB 上（见文件头用法）")
	}
	pkiDir := os.Getenv("EHOME_REAL_PKI")
	if pkiDir == "" {
		t.Fatal("EHOME_REAL_PKI 未设置（需要 ca.crt / server.crt / server.key）")
	}
	addr := os.Getenv("EHOME_REAL_ADDR")
	if addr == "" {
		addr = "0.0.0.0:8443"
	}
	waitSec := 150
	if v := os.Getenv("EHOME_REAL_WAIT"); v != "" {
		fmt.Sscanf(v, "%d", &waitSec)
	}

	srvCert, err := tls.LoadX509KeyPair(
		filepath.Join(pkiDir, "server.crt"), filepath.Join(pkiDir, "server.key"))
	if err != nil {
		t.Fatalf("读服务器证书失败: %v", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(pkiDir, "ca.crt"))
	if err != nil {
		t.Fatalf("读 CA 失败: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA 不是合法 PEM")
	}

	// ── 真实装配（与 startE2EServerEx 同源）──────────────────────────────
	db := testutil.OpenTestDB(t)
	reg := transport.NewRegistry()
	bridge := downlink.New(reg)
	hub := websocket.NewHub()
	go hub.Run()
	mgr := nodemgr.NewManager(db, bridge, hub, nil, nil)

	cfg := transport.Config{
		Addr:             addr,
		Cert:             srvCert,
		ClientCAs:        pool,
		HandshakeTimeout: 10 * time.Second,
		OnFrame:          mgr.FrameHandler(),
		Registry:         reg, // ⚠ 必须与 bridge 同一个对象，否则下行找不到会话
	}
	srv, err := transport.New(cfg)
	if err != nil {
		t.Fatalf("transport.New: %v", err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen(%s): %v —— 端口被占/权限不足", addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); _ = srv.Close() }()
	go func() { _ = srv.Serve(ctx) }()
	t.Logf("后端 mTLS 已就绪: %s（等设备连接，最多 %d 秒）", srv.Addr(), waitSec)

	// ── 等设备连上来并完成 mTLS ───────────────────────────────────────────
	deadline := time.Now().Add(time.Duration(waitSec) * time.Second)
	var nodeID string
	for time.Now().Before(deadline) {
		if ids := reg.Nodes(); len(ids) > 0 {
			nodeID = ids[0]
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if nodeID == "" {
		fr, bad, resync, tooLarge, badCRC, _ := srv.StatsSnapshot()
		t.Fatalf("**设备始终没有连上来**（%d 秒）。\n"+
			"传输层统计: frames=%d badHeader=%d resync=%d tooLarge=%d badCRC=%d\n"+
			"判读：全 0 ⇒ 连接未建立（网段/DNS/端口/服务器证书 SAN）；\n"+
			"      badHeader>0 ⇒ 连上了但帧头不对（成帧/魔数）；\n"+
			"      badCRC>0 ⇒ CRC 位与实际不符",
			waitSec, fr, bad, resync, tooLarge, badCRC)
	}
	t.Logf("✅ 设备已通过 **mTLS 双向认证** 并被登记: nodeID=%q", nodeID)

	// ── 下行：证明"发得回去" ─────────────────────────────────────────────
	// 手造一条 HelloAck（0x12）。⚠ 生产路径的 HelloAck 由 nodemgr.handleHello
	// 经 downlink.Bridge 产出，那条路由 device_e2e_test.go 覆盖；
	// 这里只需一条合法下行来证明"设备在、能发回去"。
	enc := frame.NewEncoder(frame.MsgHelloAck)
	enc.EncodeVarint(1, uint64(time.Now().Unix()))
	enc.EncodeString(2, nodeID)
	payload := enc.Bytes()
	res := srv.Send(nodeID, payload)
	t.Logf("下行 HelloAck: result=%v bytes=%d", res, len(payload))
	if res != transport.SendOK {
		t.Errorf("下行失败 result=%v —— 设备在，但发不回去（会话/注册表问题）", res)
	}

	time.Sleep(3 * time.Second)
	fr, bad, resync, tooLarge, badCRC, overflow := srv.StatsSnapshot()
	t.Logf("传输层统计: frames=%d badHeader=%d resync=%d tooLarge=%d badCRC=%d overflow=%d",
		fr, bad, resync, tooLarge, badCRC, overflow)
	if fr == 0 {
		t.Error("传输层收到 0 帧 —— 设备连上了却什么都没发（或帧全被拒）")
	}
}
