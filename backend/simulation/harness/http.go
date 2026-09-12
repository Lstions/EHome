//go:build simulation

package harness

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// envelope 是后端统一响应信封（backend/internal/api/envelope.go）。
//
// 与设计 §5.2 的偏差（照实修正，已在报告中列出）：
// 设计写的是 "Code string // 信封 code（成功码）"，而真实代码里
// envelope.code 是 **int**（数值等于 HTTP 状态码），机器可读原因是
// 独立的 error_code 字符串字段。此处按真实类型实现。
type envelope struct {
	Code      *int            `json:"code"`
	Data      json.RawMessage `json:"data"`
	Message   string          `json:"message"`
	ErrorCode string          `json:"error_code"`
}

// Session 是一个独立的 HTTP 会话（可匿名、可携带令牌）。
// 它是"真实用户视角"的载体：所有场景断言都必须经由它发出真实 HTTP 请求。
type Session struct {
	env     *Env
	BaseURL string
	Token   string

	client  *http.Client
	headers map[string]string
}

// HTTPClient 暴露底层 client，供 ws/upload 等助手复用。
func (s *Session) HTTPClient() *http.Client { return s.client }

// NewSession 新建匿名会话（无 Authorization 头）。
// 匿名会话用于验证"未鉴权"路径：探针端点可达性、401 语义等。
func (e *Env) NewSession() *Session {
	return &Session{
		env:     e,
		BaseURL: e.BaseURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
			// DisableCompression：不发送 Accept-Encoding: gzip。
			//
			// 为什么：main.go 给全部路由挂了 gin-contrib/gzip，而 /metrics 的
			// promhttp.Handler 在客户端声明 Accept-Encoding: gzip 时**自己也压缩**
			// 一次。两层压缩叠加后，Go 的 http.Transport 只做一次透明解压，
			// 留下的载荷无法用标准 gzip 解析（详见 SIM-DEP-005 的证据与报告）。
			// 断言必须建立在确定、可读的响应体上；压缩路径本身由浏览器 E2E
			// 层覆盖，不属于本框架的验证目标。
			Transport: &http.Transport{DisableCompression: true},
		},
		headers: map[string]string{},
	}
}

// Session 新建独立会话并登录（设计 §5.1）。
// 返回 error 而不是直接失败，便于场景断言"某些凭据必须登录失败"。
func (e *Env) Session(user, pass string) (*Session, error) {
	s := e.NewSession()
	if err := s.Login(user, pass); err != nil {
		return nil, err
	}
	return s, nil
}

// Login 是 Session 的同义入口（契约同时列出了两个名字）。
func (e *Env) Login(user, pass string) (*Session, error) { return e.Session(user, pass) }

// Login 在既有会话上执行登录并写入令牌。
// 返回 error 时不做任何断言：调用方可能正想验证登录失败。
func (s *Session) Login(user, pass string) error {
	resp := s.Post("/api/v1/auth/login", map[string]any{"username": user, "password": pass})
	if resp.Status != http.StatusOK {
		return fmt.Errorf("登录失败: status=%d message=%q body=%s", resp.Status, resp.Message, resp.BodyString())
	}
	token, err := resp.String("token")
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("登录响应未携带令牌: %s", resp.BodyString())
	}
	s.Token = token
	return nil
}

// WithHeader 在会话上追加固定请求头（链式），返回自身便于内联使用。
func (s *Session) WithHeader(name, value string) *Session {
	s.headers[name] = value
	return s
}

// Response 是一次真实 HTTP 往返的完整记录。
//
// 设计 §5.2 的强制要求：所有断言必须走 Expect*/Data*，
// 不得在场景里手写 if resp.Status != 200 { t.Fatalf }。
// 这里把"失败上下文"（method/path/status/body/耗时）集中在一处渲染。
type Response struct {
	Method    string
	Path      string
	Status    int
	Code      int // 信封 code（真实实现为 int；未解析出信封时等于 Status）
	ErrorCode string
	Message   string
	Data      json.RawMessage
	Raw       []byte
	Duration  time.Duration
	Header    http.Header

	session    *Session
	isEnvelope bool
}

// BodyString 返回响应体文本（截断到 4KiB，避免日志被大响应淹没）。
func (r *Response) BodyString() string {
	const limit = 4096
	body := string(r.Raw)
	if len(body) > limit {
		return body[:limit] + fmt.Sprintf("…(截断，共 %d 字节)", len(r.Raw))
	}
	return body
}

// context 渲染失败上下文：method/path/status/body/耗时 一个都不少。
func (r *Response) context() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s → HTTP %d（耗时 %s）", r.Method, r.Path, r.Status, r.Duration.Round(time.Microsecond))
	if r.isEnvelope {
		fmt.Fprintf(&b, "\n信封: code=%d message=%q error_code=%q", r.Code, r.Message, r.ErrorCode)
	}
	fmt.Fprintf(&b, "\n响应体: %s", r.BodyString())
	return b.String()
}

// fail 统一走 Env.Fatalf，保证失败会被记入 summary.json 的 failure 字段。
func (r *Response) fail(err error) {
	if r.session == nil || r.session.env == nil {
		panic("harness: Response 未绑定 Env: " + err.Error())
	}
	r.session.env.Fatalf("%v\n%s", err, r.context())
}

// Check 是非致命断言：返回 error 而不是终止测试。
// 供 Eventually 的 cond 使用（轮询期间失败是正常的，不算断言失败）。
func (r *Response) Check(status int) error {
	if r.Status != status {
		return fmt.Errorf("期望 HTTP %d，实际 %d；message=%q body=%s", status, r.Status, r.Message, r.BodyString())
	}
	return nil
}

// Expect 断言 HTTP 状态码，失败附带完整上下文与耗时。
func (r *Response) Expect(status int) *Response {
	if err := r.Check(status); err != nil {
		r.fail(err)
	}
	return r
}

// CheckError 是非致命版错误断言：要求状态码匹配，且（当 code 非空时）
// 信封 error_code 精确匹配。
func (r *Response) CheckError(status int, code string) error {
	if err := r.Check(status); err != nil {
		return err
	}
	if code != "" && r.ErrorCode != code {
		return fmt.Errorf("期望 error_code=%q，实际 %q（message=%q body=%s）", code, r.ErrorCode, r.Message, r.BodyString())
	}
	return nil
}

// ExpectError 断言错误语义：状态码 + 机器可读 error_code。
func (r *Response) ExpectError(status int, code string) *Response {
	if err := r.CheckError(status, code); err != nil {
		r.fail(err)
	}
	return r
}

// Decode 把 data 段反序列化到 out。
func (r *Response) Decode(out any) *Response {
	if len(r.Data) == 0 || string(r.Data) == "null" {
		r.fail(fmt.Errorf("响应 data 为空，无法解码"))
		return r
	}
	if err := json.Unmarshal(r.Data, out); err != nil {
		r.fail(fmt.Errorf("解码 data 失败: %w", err))
	}
	return r
}

// HeaderValue 读取响应头（如 Retry-After）。
func (r *Response) HeaderValue(name string) string {
	if r.Header == nil {
		return ""
	}
	return r.Header.Get(name)
}

// ---------- 点路径取值 ----------

// Value 按点路径取值。支持 a.b.c 与数组下标 values.0.sensor_name；
// 路径不存在时返回 error（不静默返回零值）。
func (r *Response) Value(path string) (any, error) {
	if !r.isEnvelope {
		return nil, fmt.Errorf("响应不是标准信封，无法按路径取值: %s", r.BodyString())
	}
	var current any
	if err := json.Unmarshal(r.Data, &current); err != nil {
		return nil, fmt.Errorf("解析 data 失败: %w (data=%s)", err, string(r.Data))
	}
	for _, segment := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[segment]
			if !ok {
				return nil, fmt.Errorf("data 中不存在路径 %q（段 %q 缺失）", path, segment)
			}
			current = value
		case []any:
			idx, err := strconv.Atoi(segment)
			if err != nil {
				return nil, fmt.Errorf("data 路径 %q 的段 %q 不是合法数组下标", path, segment)
			}
			if idx < 0 || idx >= len(node) {
				return nil, fmt.Errorf("data 路径 %q 下标 %d 越界（长度 %d）", path, idx, len(node))
			}
			current = node[idx]
		default:
			return nil, fmt.Errorf("data 路径 %q 在段 %q 处不可继续下钻（当前类型 %T）", path, segment, current)
		}
	}
	return current, nil
}

// String 按路径取字符串（非致命）。
func (r *Response) String(path string) (string, error) {
	value, err := r.Value(path)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("data 路径 %q 期望字符串，实际 %T", path, value)
	}
	return text, nil
}

// Int 按路径取整数（非致命）。JSON 数字统一为 float64，这里做整数校验。
func (r *Response) Int(path string) (int64, error) {
	value, err := r.Value(path)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("data 路径 %q 期望数字，实际 %T", path, value)
	}
	if number != float64(int64(number)) {
		return 0, fmt.Errorf("data 路径 %q 期望整数，实际 %v", path, number)
	}
	return int64(number), nil
}

// Float 按路径取浮点数（非致命）。
func (r *Response) Float(path string) (float64, error) {
	value, err := r.Value(path)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("data 路径 %q 期望数字，实际 %T", path, value)
	}
	return number, nil
}

// Bool 按路径取布尔（非致命）。
func (r *Response) Bool(path string) (bool, error) {
	value, err := r.Value(path)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("data 路径 %q 期望布尔，实际 %T", path, value)
	}
	return flag, nil
}

// Slice 按路径取数组（非致命）。
func (r *Response) Slice(path string) ([]any, error) {
	value, err := r.Value(path)
	if err != nil {
		return nil, err
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("data 路径 %q 期望数组，实际 %T", path, value)
	}
	return items, nil
}

// ---------- 契约要求的致命包装（缺路径即失败） ----------

// DataString 按点路径取字符串，缺路径即失败。
func (r *Response) DataString(path string) string {
	value, err := r.String(path)
	if err != nil {
		r.fail(err)
	}
	return value
}

// DataInt 按点路径取整数，缺路径即失败。
func (r *Response) DataInt(path string) int64 {
	value, err := r.Int(path)
	if err != nil {
		r.fail(err)
	}
	return value
}

// DataFloat 按点路径取浮点数，缺路径即失败。
func (r *Response) DataFloat(path string) float64 {
	value, err := r.Float(path)
	if err != nil {
		r.fail(err)
	}
	return value
}

// DataBool 按点路径取布尔，缺路径即失败。
func (r *Response) DataBool(path string) bool {
	value, err := r.Bool(path)
	if err != nil {
		r.fail(err)
	}
	return value
}

// DataSlice 按点路径取数组，缺路径即失败。
func (r *Response) DataSlice(path string) []any {
	value, err := r.Slice(path)
	if err != nil {
		r.fail(err)
	}
	return value
}

// ---------- 请求发送 ----------

// Do 发送一次请求并记录完整往返信息。
// body 支持 nil / []byte / string / 任意可 JSON 序列化值。
func (s *Session) Do(method, path string, body any) *Response {
	return s.do(method, path, body, "")
}

func (s *Session) do(method, path string, body any, contentType string) *Response {
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(value)
		if contentType == "" {
			contentType = "application/json"
		}
	case string:
		reader = strings.NewReader(value)
		if contentType == "" {
			contentType = "application/json"
		}
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			s.env.Fatalf("序列化请求体失败 %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(encoded)
		if contentType == "" {
			contentType = "application/json"
		}
	}

	requestURL := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		requestURL = s.BaseURL + path
	}
	req, err := http.NewRequest(method, requestURL, reader)
	if err != nil {
		s.env.Fatalf("构造请求失败 %s %s: %v", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	for name, value := range s.headers {
		req.Header.Set(name, value)
	}

	start := time.Now()
	httpResp, err := s.client.Do(req)
	duration := time.Since(start)
	if err != nil {
		s.env.Fatalf("HTTP 请求失败 %s %s（耗时 %s）: %v", method, path, duration.Round(time.Microsecond), err)
	}
	defer httpResp.Body.Close()
	raw, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		s.env.Fatalf("读取响应体失败 %s %s: %v", method, path, readErr)
	}
	raw = unwrapGzip(raw)

	resp := &Response{
		Method:   method,
		Path:     path,
		Status:   httpResp.StatusCode,
		Code:     httpResp.StatusCode,
		Raw:      raw,
		Duration: duration,
		Header:   httpResp.Header,
		session:  s,
	}
	resp.parseEnvelope()
	return resp
}

// unwrapGzip 逐层剥掉响应体外的 gzip 包装。
//
// 为什么需要：main.go 给全部路由挂了 gin-contrib/gzip，而 /metrics 的
// promhttp.Handler 本身也会在客户端声明 Accept-Encoding: gzip 时压缩。
// Go 的 http.Transport 会自动加上该头并做**一次**透明解压，
// 于是最终拿到的是"解压一次后仍是 gzip 字节"的载荷，且响应头里的
// Content-Encoding 已被 Transport 摘掉 —— 只按响应头判断会漏掉它。
//
// 判据用 gzip 魔数（1f 8b）而不是响应头：harness 的 Response 只承载
// JSON / 文本（二进制下载走 DownloadRaw，不经过这里），因此"以魔数开头"
// 只可能是压缩包装。解压失败一律保留原字节，绝不吞内容。
// lastGzipError 记录最近一次解包失败的原因，供 format 类断言输出诊断
// （gzip 相关失败如果不带上原因，现场只剩"一堆乱码"，无法定位）。
var lastGzipError string

// GzipDiagnostic 返回最近一次解包失败的原因（无失败时为空串）。
func GzipDiagnostic() string { return lastGzipError }

func unwrapGzip(raw []byte) []byte {
	lastGzipError = ""
	const maxLayers = 3
	for layer := 0; layer < maxLayers; layer++ {
		if !hasGzipHeader(raw) {
			return raw
		}
		// 首选标准 gzip 解压（带 CRC 校验）。
		if decompressed, err := inflateGzip(raw); err == nil {
			raw = decompressed
			continue
		} else {
			lastGzipError = err.Error()
		}
		// 回退：跳过连续重复的 gzip 头后按裸 deflate 流解压。
		//
		// 这条回退路径对应一个真实观测（见 SIM-DEP-005 的证据与任务报告）：
		// gin-contrib/gzip 与 promhttp.Handler 各自写了 gzip 头，但只产出
		// 一条 deflate 流，于是客户端做过一次透明解压后拿到的是
		// "头1 || 头2 || deflate" —— 标准 gzip 解析器会在头 1 之后
		// 读到 0x1f（BTYPE=3，deflate 保留值）而报 corrupt input。
		if decompressed, err := inflateAfterHeaders(raw); err == nil {
			raw = decompressed
			continue
		} else {
			lastGzipError = lastGzipError + " / fallback: " + err.Error()
			return raw
		}
	}
	return raw
}

func hasGzipHeader(raw []byte) bool {
	return len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b
}

// gzipHeaderLength 是 RFC 1952 固定头长度（不含 FEXTRA/FNAME 等可选段）。
const gzipHeaderLength = 10

func inflateGzip(raw []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("gzip header: %w", err)
	}
	defer reader.Close()
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("gzip body: %w", err)
	}
	if len(decompressed) == 0 {
		return nil, fmt.Errorf("gzip body: 解压结果为空")
	}
	return decompressed, nil
}

func inflateAfterHeaders(raw []byte) ([]byte, error) {
	trimmed := raw
	headers := 0
	for hasGzipHeader(trimmed) {
		if len(trimmed) <= gzipHeaderLength {
			return nil, fmt.Errorf("只剩 gzip 头（跳过了 %d 个）", headers)
		}
		trimmed = trimmed[gzipHeaderLength:]
		headers++
	}
	if headers == 0 {
		return nil, fmt.Errorf("没有可跳过的 gzip 头")
	}
	reader := flate.NewReader(bytes.NewReader(trimmed))
	defer reader.Close()
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("裸 deflate（跳过 %d 个头后）: %w", headers, err)
	}
	if len(decompressed) == 0 {
		return nil, fmt.Errorf("裸 deflate（跳过 %d 个头后）: 结果为空", headers)
	}
	return decompressed, nil
}

// parseEnvelope 尽力解析统一信封。非信封端点（/health、/metrics、/ping）
// 保持 isEnvelope=false，Data* 系列会给出"响应不是标准信封"的明确诊断。
func (r *Response) parseEnvelope() {
	var parsed envelope
	if err := json.Unmarshal(r.Raw, &parsed); err != nil {
		return
	}
	// 信封判别：至少要有 code 或 message 字段，否则视为裸 JSON。
	if parsed.Code == nil && parsed.Message == "" {
		return
	}
	r.isEnvelope = true
	if parsed.Code != nil {
		r.Code = *parsed.Code
	}
	r.Message = parsed.Message
	r.ErrorCode = parsed.ErrorCode
	r.Data = parsed.Data
}

// Get 发送 GET。
func (s *Session) Get(path string) *Response { return s.Do(http.MethodGet, path, nil) }

// Post 发送 POST。
func (s *Session) Post(path string, body any) *Response {
	return s.Do(http.MethodPost, path, body)
}

// Put 发送 PUT。
func (s *Session) Put(path string, body any) *Response {
	return s.Do(http.MethodPut, path, body)
}

// Patch 发送 PATCH。
func (s *Session) Patch(path string, body any) *Response {
	return s.Do(http.MethodPatch, path, body)
}

// Delete 发送 DELETE。
func (s *Session) Delete(path string) *Response { return s.Do(http.MethodDelete, path, nil) }

// GetQuery 发送带查询参数的 GET（自动 URL 编码）。
func (s *Session) GetQuery(path string, query url.Values) *Response {
	if len(query) > 0 {
		path = path + "?" + query.Encode()
	}
	return s.Get(path)
}

// DecodeJWTPayload 解码 JWT 的 payload 段（不校验签名）。
//
// 用途：SIM-AUTH-008 需要断言"记住我"签发更长有效期。签发时长是令牌
// 自身的公开属性（exp - iat），解码 payload 即可验证；场景不去也不该去
// 触碰签名密钥。
func DecodeJWTPayload(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("不是三段式 JWT（段数 %d）", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("解码 JWT payload 失败: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("解析 JWT payload 失败: %w", err)
	}
	return claims, nil
}
