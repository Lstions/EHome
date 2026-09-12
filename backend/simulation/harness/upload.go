//go:build simulation

package harness

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// UploadFile 描述 multipart 表单里的一个文件字段。
type UploadFile struct {
	// Field 是表单字段名（例如固件上传的 "file"）。
	Field string
	// Filename 是客户端文件名，服务端会据此做扩展名校验。
	Filename string
	// Content 是文件字节。
	Content []byte
}

// UploadMultipart 发送一次 multipart/form-data 请求。
//
// 为什么放在 harness：固件上传、批量导入等端点都要求 multipart，
// 而 Response 的统一失败上下文（method/path/status/body/耗时）必须在
// 这条路里同样生效，否则上传类场景的失败会退化成"看不出服务端说了什么"。
// files 里的字段按切片顺序写入；fields 是普通文本字段。
func (s *Session) UploadMultipart(path string, fields map[string]string, files []UploadFile) *Response {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// 固定文本字段顺序，保证请求可复现（map 遍历顺序随机）。
	for _, name := range sortedKeys(fields) {
		if err := writer.WriteField(name, fields[name]); err != nil {
			s.env.Fatalf("写入 multipart 字段 %s 失败: %v", name, err)
		}
	}
	for _, file := range files {
		part, err := writer.CreateFormFile(file.Field, file.Filename)
		if err != nil {
			s.env.Fatalf("创建 multipart 文件字段 %s 失败: %v", file.Field, err)
		}
		if _, err := part.Write(file.Content); err != nil {
			s.env.Fatalf("写入 multipart 文件 %s 失败: %v", file.Filename, err)
		}
	}
	if err := writer.Close(); err != nil {
		s.env.Fatalf("关闭 multipart 写入器失败: %v", err)
	}

	return s.do(http.MethodPost, path, body.Bytes(), writer.FormDataContentType())
}

// UploadFile 是单文件上传的便捷包装（无文本字段）。
func (s *Session) UploadFile(path, field, filename string, content []byte) *Response {
	return s.UploadMultipart(path, nil, []UploadFile{{Field: field, Filename: filename, Content: content}})
}

// Upload 是"一个文件 + 若干文本字段"的便捷包装。
// 固件上传（POST /api/v1/firmwares/upload，服务端用 c.FormFile("file") 取件）
// 是它的典型调用方。
func (s *Session) Upload(path string, fields map[string]string, field, filename string, content []byte) *Response {
	return s.UploadMultipart(path, fields, []UploadFile{{Field: field, Filename: filename, Content: content}})
}

// DownloadURL 对任意 URL 发一次带当前会话令牌的 GET，
// 用于验证"带票据/不带票据的下载"这类鉴权差异。
func (s *Session) DownloadURL(rawURL string, withToken bool) *Response {
	saved := s.Token
	if !withToken {
		s.Token = ""
	}
	defer func() { s.Token = saved }()
	return s.Get(rawURL)
}

// DownloadRaw 对任意 URL 发一次裸 GET（不携带令牌、不解析信封），
// 返回状态码与响应体字节。供票据下载场景断言二进制内容。
func (s *Session) DownloadRaw(rawURL string) (int, []byte, time.Duration, error) {
	saved := s.Token
	s.Token = ""
	defer func() { s.Token = saved }()

	start := time.Now()
	resp, err := s.client.Get(rawURL)
	duration := time.Since(start)
	if err != nil {
		return 0, nil, duration, fmt.Errorf("GET %s 失败: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, nil, duration, fmt.Errorf("读取 %s 响应体失败: %w", rawURL, readErr)
	}
	return resp.StatusCode, body, duration, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	// 插入排序：字段数量极少（<10），避免为一个稳定顺序引入 sort 依赖噪音。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
