# 部署黑盒验证 — 编排结果 (D-20260916-103037)

- 时间: 20260916-103037
- 仓库: /home/sun/workspace/EHomeSystem @ 9c8214d8
- project: ehome-bb  端口: 18080  独立库: ehome_bb_20260916_103037
- 选项: --only 全部 / --skip-build=0
- 退出码: **3**  (0=全绿 1=失败/残留/共享异常 2=有 SKIP 未完成 3=并发冲突未跑成)

## 命题结果

| id | 命题 | 探针 | 结论 | 说明 | 证据 |
|---|---|---|---|---|---|
| P0 | ENV project-lock | - | **CONFLICT** | project ehome-bb 排他锁被占用 — 本 run 未起栈、未清理他人资源 | - |

## 环境护栏

| 检查 | 结果 |
|---|---|
| 零残留 (容器/卷/网络/独立库) | PASS |
| 共享资源未受影响 (:3080 / :8082 / 4 容器) | PASS |

## 未覆盖 (方案 §6 照抄)

- 不做真实 ESP32 硬件验证
- 不做生产环境验证 (生产域名/证书/反代不在范围)
- 不做压力/容量测试
- 不做镜像安全扫描 (只查无密钥泄漏)
- 不覆盖 nginx 反代路径 (80 端口被占用, 本方案不经 nginx)

## 复现命令

    cd /home/sun/workspace/EHomeSystem
    ./deploy/blackbox/run.sh              # 全量
    ./deploy/blackbox/run.sh --only 04    # 单条
    cat /home/sun/workspace/EHomeSystem/deploy/blackbox/evidence/D-20260916-103037/run.log                     # 完整编排日志
