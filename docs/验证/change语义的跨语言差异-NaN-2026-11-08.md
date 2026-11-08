# change 语义的跨语言差异：NaN（2026-11-08）

## 一句话

`unified_data` 的 SQL `lag() IS DISTINCT FROM` 与 Go 的 `!=` 对 **NaN** 的语义相反，
所以「连续两帧都是 NaN」时两条路会分歧。生产实测 0 行 NaN，所以现在不触发。

## 实测（PostgreSQL 18）

```
NaN = NaN                => true      ← SQL 标准（与 IEEE754 相反）
NaN IS DISTINCT FROM NaN => false
NULL IS DISTINCT FROM 0  => true
```

Go 的 `!=` 遵循 IEEE754：`NaN != NaN` 是 **true**。

## 分歧形态

```
若某字段连续两帧都是 NaN：

  SQL: NaN IS DISTINCT FROM NaN => false => 不输出
  Go : NaN != NaN               => true  => 输出

  => 两条路的 change 点数不同 => 逐点对拍必然红
```

## 生产现状（只读实测）

```
unified_data 中 value = NaN      : 0 行
unified_data 中 value = ±Inf     : 0 行
值域                              : -48.9 .. 4786.4
telemetry_frame 中空 payload      : 0 行
```

## 为什么现在不会有 NaN（结构性原因）

帧表是**量化编码**：`FieldSpec.Quantize` 把 float 映射成整数码点，
`round(NaN)` 未定义 ⇒ **NaN 在帧表里物理上无法表达**。

而 `unified_data.value` 是 `float8` 列，可以存 NaN。

```
这是一个表结构决定的不对称：
  unified_data 能存 NaN，帧表存不了
  => 若 NaN 曾进过 unified_data，帧表那条路根本没有对应的值
  => 实测 0 行，所以没发生 —— 但这是「运气 + 上游没产出 NaN」，不是结构保证
```

## 处置

**不是修实现，是写清边界。** 在 `aggregateFramePoints` 的 change 分支上方记录：

```go
// ============ change 语义：SQL 与 Go 的一处结构性差异（NaN）============
//
// SQL: lag(v) IS DISTINCT FROM v  —— 对 NaN 遵循 SQL 标准：NaN IS NOT DISTINCT FROM NaN
// Go : raw[i].Value != prev        —— 对 NaN 遵循 IEEE754：NaN != NaN 为 true
//
// 所以「连续两帧都是 NaN」时：SQL 不输出第二个，Go 会输出 —— 两条路分歧。
//
// 为什么现在不是问题：
//   · 帧表是量化编码（Quantize 把 float 映射成整数码点），NaN 物理上无法表达；
//   · 生产实测 unified_data 中 value=NaN 的行数 = 0（值域 -48.9..4786.4，无 ±Inf）。
//
// 若将来出现 NaN（例如某个解析器产出 NaN 且写入路径未拦截），
// 帧表侧会在编码时就被 Clamp/丢弃，unified_data 侧会存下来 —— 那时两条路必然不等。
// 这不是本实现能修的，是 L2 量化编码的固有边界。
```

## 为什么这条必须记录而不是「加个用例」

```
加用例：需要造 NaN 的 unified_data 行 => 但帧表侧造不出来（量化）=> 无法对拍
      => 只能写一条「复现差异」的用例，而那条用例的价值是「证明差异存在」，
         不是「防止回归」
记录 ：明确告诉后来人「这里有个已知差异，以及它为什么不触发」
      => 若将来触发，他会先看到这段注释，而不是先怀疑解码器
```

**有些边界不能被用例守住，只能被注释守住。这需要如实说明，不能假装用例覆盖了它。**

## 复现

`.tmp-telemetry/probe_nan.sh`（只读；DROP 后失效）。
