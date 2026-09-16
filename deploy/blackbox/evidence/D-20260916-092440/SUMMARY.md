# 部署黑盒验证 — 编排结果 (D-20260916-092440)

- 时间: 20260916-092440
- 仓库: /home/sun/workspace/EHomeSystem @ 606d8e6f
- project: ehome-bb  端口: 18080  独立库: ehome_bb_20260916_092440
- 选项: --only 全部 / --skip-build=0
- 退出码: **1**  (0=全绿 1=失败/残留/共享异常 2=有 SKIP 未完成)

## 命题结果

| id | 命题 | 探针 | 结论 | 说明 | 证据 |
|---|---|---|---|---|---|
| 01 | Q1 image-contract | 缺失 | **SKIP** | 探针文件缺失: /tmp/bb-empty-probes/01-*.sh | - |
| 07 | Q7 reproducible | 缺失 | **SKIP** | 探针文件缺失: /tmp/bb-empty-probes/07-*.sh | - |
| 02 | Q2 env-contract | 缺失 | **SKIP** | 探针文件缺失: /tmp/bb-empty-probes/02-*.sh | - |
| 03 | Q3 coldstart | 缺失 | **SKIP** | 探针文件缺失: /tmp/bb-empty-probes/03-*.sh | - |
| 06 | Q6 wiring | 缺失 | **SKIP** | 探针文件缺失: /tmp/bb-empty-probes/06-*.sh | - |
| 04 | Q4 health | 缺失 | **SKIP** | 探针文件缺失: /tmp/bb-empty-probes/04-*.sh | - |
| 05 | Q5 first-boot | 缺失 | **SKIP** | 探针文件缺失: /tmp/bb-empty-probes/05-*.sh | - |
| S1 | SUP cache-warmup | - | **SKIP** | 栈未起来, 无冷启动日志 | /home/sun/workspace/EHomeSystem/deploy/blackbox/evidence/D-20260916-092440/S1-cache-warmup |

## 环境护栏

| 检查 | 结果 |
|---|---|
| 零残留 | DIRTY — 见 98-zero-residue/residue.txt |
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
    cat /home/sun/workspace/EHomeSystem/deploy/blackbox/evidence/D-20260916-092440/run.log                     # 完整编排日志
