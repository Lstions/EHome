package notify

import (
	"context"

	"ehome/backend/internal/models"
)

// Notifier 是通知写入点的**窄注入面** (设计 §2.1 取向 A 的接线契约)。
//
// 为什么是窄接口而不是把 *Dispatcher 注入 4 个包:
//   - 本仓既有范式是"用窄接口/函数变量二阶段注入, 避免包间编译期依赖"
//     (nodemgr.SetAlertEvaluator / SetAutomationEvaluator / SetLatestSinkFn,
//     datasource.Service.SetNotifier 已是 func(models.Notification))。
//     automation / alert / datalifecycle 直接 import *notify.Dispatcher 会新引入一条
//     "业务包 → outbound HTTP 引擎"的编译期依赖, 与既有取向相反。
//   - 窄接口让写入点的测试只需一个 fake, 不必构造真实的 db+client。
//   - *Dispatcher 的实现面 (Deliver/CreateAsync/Outcome/...) 远比写入点需要的多;
//     写入点需要的只有"落库 + 顺带投递"这一件事。
//
// 契约 (与 Dispatcher.Create 完全一致, fail-open):
//   - 实现必须**同步完成落库**后才返回 —— 调用方依赖返回后 n.ID 可用
//     (广播载荷 / 幂等序号都要它);
//   - 实现**不得返回 error、不得 panic**: 通知失败绝不影响主流程
//     (设计 §2.2, 既有 datasource/migrate 的 fail-open 铁律);
//   - 实现负责"落库 + 投递", 调用方不再自行 Create。
//
// 投递时机裁决见 dispatcher.go 的 Create/CreateAsync 注释。
type Notifier interface {
	Create(ctx context.Context, n *models.Notification)
}

// 编译期断言: Dispatcher 必须满足写入点的注入契约。
// 放在非 _test 文件里, 让"Dispatcher 改了签名而写入点还在用"在 build 阶段就红,
// 而不是等到某个包的 setter 赋值失败才发现。
var _ Notifier = (*Dispatcher)(nil)
