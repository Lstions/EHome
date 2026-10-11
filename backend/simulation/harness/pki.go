//go:build simulation

package harness

// pki.go —— 仿真用的 mTLS 证书（2026-10-11，随 TestScenarios 从 MQTT 移植到 3.0）。
//
// # 为什么需要它
//
// 3.0 的设备传输是 mTLS 长连接，后端从**客户端证书的 CN** 取 node_id
// （internal/transport/server.go:307 nodeIDFromCert）。MQTT 时代仿真设备只要一个
// broker 地址即可上线；现在它必须持有一张被服务端 CA 签发的证书。
//
// # 与 device_e2e_test.go 的 newE2EPKI 的关系
//
// 那份 PKI 在 cmd/server 包里（服务于单元测试，用 *testing.T 报错）。
// harness 在**子进程**语境下运行且不持有 t，故此处独立实现同一套逻辑，
// 并把 CA/服务端证书**落盘**（子进程需要文件路径，无法传内存对象）。
//
// ⚠ 证书只写进 runDir（临时目录），CN 一律以 sim- 开头 ——
// 与 Device.assertSimNode 的安全红线同源：绝不干扰同网段的真实设备。

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// simPKI 是一次仿真运行的证书材料。
type simPKI struct {
	// dir 是证书落盘目录（runDir/pki）。
	dir string
	// caFile 是设备端用于校验服务端的 CA（client_ca_file 也用它）。
	caFile string
	// serverCertFile / serverKeyFile 是后端监听用的服务端证书。
	serverCertFile string
	serverKeyFile  string

	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	caPEM   []byte
}

// newSimPKI 生成一套 CA + 服务端证书，落盘到 dir，供后端子进程使用。
//
// 设备证书**按需签发**（deviceCert），因为每个仿真节点都要一张 CN=其 nodeID 的证书。
func newSimPKI(dir string) (*simPKI, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 PKI 目录失败: %w", err)
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 CA 私钥失败: %w", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ehome-sim-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("签发 CA 失败: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("解析 CA 失败: %w", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	p := &simPKI{dir: dir, caCert: caCert, caKey: caKey, caPEM: caPEM,
		caFile: filepath.Join(dir, "ca.crt")}
	if err := os.WriteFile(p.caFile, caPEM, 0o644); err != nil {
		return nil, fmt.Errorf("写 CA 失败: %w", err)
	}

	// 服务端证书：SAN 必须包含 127.0.0.1（设备端用 tls.Dial 连它）。
	serverDER, serverKey, err := p.issue("ehome-server", true)
	if err != nil {
		return nil, err
	}
	p.serverCertFile = filepath.Join(dir, "server.crt")
	p.serverKeyFile = filepath.Join(dir, "server.key")
	if err := os.WriteFile(p.serverCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), 0o644); err != nil {
		return nil, fmt.Errorf("写服务端证书失败: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		return nil, fmt.Errorf("序列化服务端私钥失败: %w", err)
	}
	if err := os.WriteFile(p.serverKeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, fmt.Errorf("写服务端私钥失败: %w", err)
	}
	return p, nil
}

// issue 用 CA 签一张叶子证书；server=true 走 ServerAuth，否则 ClientAuth。
func (p *simPKI) issue(cn string, server bool) ([]byte, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成 %s 私钥失败: %w", cn, err)
	}
	eku := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if server {
		eku = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		// SAN 同时给 localhost 与 127.0.0.1：设备端两种连法都能过校验。
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("签发 %s 失败: %w", cn, err)
	}
	return der, key, nil
}

// devCert 为某个仿真节点签发客户端证书，返回 (certPEM, keyPEM)。
// CN 即 node_id —— 后端 nodeIDFromCert 直接取 CN。
func (p *simPKI) devCert(nodeID string) (cb []byte, kb []byte, err error) {
	der, key, err := p.issue(nodeID, false)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("序列化设备私钥失败: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}
