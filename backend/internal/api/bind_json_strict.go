package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
)

// bindJSONStrict 与 c.ShouldBindJSON 相同，但**拒绝未知字段**。
//
// 为什么需要它（2026-10-04 实错）：
//
// 日志开关端点的字段名是 `stream_enabled`，而运维/前端很容易顺手写成 `enabled`
// （它的姊妹端点 /log-persist 用的正是 `enabled`，两个端点字段名不一致）。
// gin 默认**静默忽略**未知字段，于是：
//
//	PUT /log-config  {"enabled": false, "level": 2}
//	-> 200 {"message":"log config updated, config sync triggered","stream_enabled":null}
//
// 调用方看到 200 就认为日志已关闭，实际只改了 level、开关纹丝未动。
// 这个"假成功"是本项目里最难查的一类缺陷：接口层没有任何异常，
// 而设备行为与预期相反，排查时会被反复误导。
//
// 选择显式拒绝而不是"兼容两种键名"，理由与 bind_to 的大小写校验一致：
// 猜测调用方意图会让"用户以为的值"和"系统实际用的值"继续分叉；
// 当场 400 并指出合法键名，才能让调用方立刻修正。
//
// 实现说明：gin 提供 binding.EnableDecoderDisallowUnknownFields 全局开关，
// 但它是**进程级全局变量**，开启后会改变所有 65 处 BindJSON 的行为，
// 属于影响面失控；这里改为局部解码，只作用于显式调用它的端点。
func bindJSONStrict(c *gin.Context, dst any) error {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return fmt.Errorf("invalid request body")
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	/* 只允许一个 JSON 值：`{...}{...}` 这类多余内容必须报错，
	 * 否则后者会被静默丢弃，又是一次"看不见的错配"。 */
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid request body: unexpected trailing content")
	}
	return nil
}
