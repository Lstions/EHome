# 部署黑盒验证 — 编排结果 (D-20260916-101908)

- 时间: 20260916-101908
- 仓库: /home/sun/workspace/EHomeSystem @ 4698d230
- project: ehome-bb  端口: 18080  独立库: ehome_bb_20260916_101908
- 选项: --only 03 / --skip-build=0
- 退出码: **0**  (0=全绿 1=失败/残留/共享异常 2=有 SKIP 未完成)

## 命题结果

| id | 命题 | 探针 | 结论 | 说明 | 证据 |
|---|---|---|---|---|---|
| 01 | Q1 image-contract | - | **SKIP** | --only 未选中 | - |
| 07 | Q7 reproducible | - | **SKIP** | --only 未选中 | - |
| 02 | Q2 env-contract | - | **SKIP** | --only 未选中 | - |
| 03 | Q3 coldstart | 03-coldstart.sh | **PASS** | exit=0 | /home/sun/workspace/EHomeSystem/deploy/blackbox/evidence/D-20260916-101908/03-coldstart |
| 06 | Q6 wiring | - | **SKIP** | --only 未选中 | - |
| 04 | Q4 health | - | **SKIP** | --only 未选中 | - |
| 05 | Q5 first-boot | - | **SKIP** | --only 未选中 | - |
| S1 | SUP cache-warmup | - | **SKIP** | 未观察到黑盒栈运行, 无冷启动日志 | /home/sun/workspace/EHomeSystem/deploy/blackbox/evidence/D-20260916-101908/S1-cache-warmup |

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
    cat /home/sun/workspace/EHomeSystem/deploy/blackbox/evidence/D-20260916-101908/run.log                     # 完整编排日志
