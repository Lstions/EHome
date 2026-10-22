# 变异自证：api 层来源组保存校验 + 删除守卫（task: SG-01/SG-03/SG-20）

> worktree：`source-group`；实现文件：`backend/internal/api/source_group_guard.go`、
> `handler_automation.go`、`handler_alert.go`、`handler_edge_device.go`、`handler_logical_device.go`；
> 测试：`backend/internal/api/source_group_guard_test.go`。
> 纪律：每次变异后跑同一组定向用例，**必须变红**；恢复后必须重新全绿。

## 基线（未变异）

```
go build ./...                                              # PASS（无输出）
go test ./internal/api/ -run 'TestSourceGroupReferenceValidation|TestEdgeDeviceDeleteReferencedByGroup|TestLogicalDeviceDeleteReferencedByGroup' -count=1
ok  ehome/backend/internal/api  0.069s
```

## 变异记录（3 个变异，覆盖 3 组断言）

### M1：短路 `resolveRuleSourceBinding` 的列校验（"五态非法输入 400" 的判据）

- 变异：在 `models.ValidateSourceRefColumns` 的 `if err != nil` 分支内插入
  `return ruleSourceBinding{}, nil // MUTATION M1`（非法两列不再返回错误，被静默接受）。
- 命令：`sed -i '/if err := models.ValidateSourceRefColumns(kind, groupID); err != nil {/a\		return ruleSourceBinding{}, nil // MUTATION M1' internal/api/source_group_guard.go`
- 结果（必须红）：
  ```
  --- FAIL: TestSourceGroupReferenceValidationFiveStates (0.01s)
  FAIL ehome/backend/internal/api
  ```
- 结论：五态 400 断言确实依赖该实现，不是若绿。

### M2：删除守卫恒放行（SG-20 的判据）

- 变异：`func guardReferences(...) bool {` 首行插入 `return true // MUTATION M2`（所有删除/合并守卫失效）。
- 命令：`sed -i 's/^func guardReferences(c \*gin.Context, info groupReferenceInfo, subject string) bool {$/&\n\treturn true \/\/ MUTATION M2/' internal/api/source_group_guard.go`
- 结果（必须红）：
  ```
  --- FAIL: TestEdgeDeviceDeleteReferencedByGroup (0.05s)
  --- FAIL: TestLogicalDeviceDeleteReferencedByGroup (0.07s)
  FAIL ehome/backend/internal/api
  ```
- 结论：单删/批量删 409 与逻辑设备合并 409 的断言均依赖守卫真实执行。

### M3：取消 automation 锚点双写（N-7 的判据）

- 变异：`rule.TriggerEdgeDeviceID = sourceBinding.AnchorEdgeDeviceID` 改为
  `rule.TriggerEdgeDeviceID = 0 // MUTATION M3`。
- 命令：`sed -i 's/^\t\t\trule.TriggerEdgeDeviceID = sourceBinding.AnchorEdgeDeviceID$/\t\t\trule.TriggerEdgeDeviceID = 0 \/\/ MUTATION M3/' internal/api/handler_automation.go`
- 结果（必须红）：
  ```
  --- FAIL: TestSourceGroupReferenceValidationGroupState (0.01s)   # 锚点断言 0 != ed.ID
  FAIL ehome/backend/internal/api
  ```
- 结论：合法绑组的"锚点双写"断言不是摆设。

## 恢复与终态校验

- 逆操作恢复：`sed -i '/MUTATION M1/d; /MUTATION M2/d'` + 把 M3 行改回锚点赋值。
- 终态证据：
  ```
  grep -rn 'MUTATION' internal/api/        # 空
  grep -n 'rule.TriggerEdgeDeviceID = sourceBinding.AnchorEdgeDeviceID' internal/api/handler_automation.go
     351: rule.TriggerEdgeDeviceID = sourceBinding.AnchorEdgeDeviceID
  go build ./...                            # PASS
  go test ./internal/api/ -run '...' -count=1
  ok  ehome/backend/internal/api  0.069s
  ```

## 诚实声明

- 第一次变异尝试用 `/tmp/sg-mutation/*.bak` 备份恢复失败（执行环境 /tmp 在不同命令间不保留），
  因此改用逆 sed 恢复并以 `grep MUTATION` + 定向用例全绿作为终态证据（见上）；未留下任何变异代码。
- 变异只覆盖本任务引入的判据（五态/组状态与锚点/删除守卫）；未对既有 handler 校验做破坏性变异。
