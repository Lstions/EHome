# msgcodec —— 测试判据（test oracle），不是生产组件

## 它为什么在这里
`msgcodec` 原先是 `components/msgcodec/` 的 3.0 骨架组件。它的实际用途只有一个：
作为**独立于生产实现**的编解码实现，与 `components/frame/frame_codec.c` 逐字节对拍
（`codec_equivalence_tests` / `golden_vectors_tests`），并承接 S0 共享向量的 C 侧验证。

生产路径从未调用它 —— 固件收发走的一直是 `frame_codec.c`。因此它在组件可达性门禁
（`tools/check_component_reachable.py`）里长期挂在"待接线清单"，2026-11-15 决策截止日
到期时必须二选一：**接进生产** 或 **降为测试用件并从组件表移除**。

## 2026-11-19 的决定：降为测试用件
选择"降级"而不是"接线"的证据：
1. 生产链路（`frame_decoder_init` + 8 个 `handler_*.c`）已经完整，且 `frame_decoder_t`
   是它们的公共输入类型；让 `msgcodec` 接管解码要改所有 handler 的输入契约，
   收益只是"换一套等价实现"，风险却不小。
2. 它的价值恰恰在于**与生产实现不同**：一旦接到生产路径上，"对拍"就退化成"自己和自己比"，
   判据价值归零。

因此本目录只被 `host_tests/CMakeLists.txt` 引用，**不参与固件构建**，也不在
`components/` 的组件表里。要恢复成生产组件，请连同"谁调用它"一起提交。
