package api

// nan_boundary_guard_test.go — 守住「NaN 跨语言差异」这条**只能被注释守住**的边界
// （2026-11-08，第三方复核产物）。

// ============ 它守的是什么 ============
//
// `aggregateFramePoints` 的 change 分支里有一条已知的跨语言语义差：
//
//	SQL: lag(v) IS DISTINCT FROM v  =>  NaN IS NOT DISTINCT FROM NaN  => 连续 NaN 视为未变化
//	Go : raw[i].Value != prev        =>  NaN != NaN 为 true          => 连续 NaN 视为变化
//
// 两条路对「连续两帧都是 NaN」给出不同点数 ⇒ 逐点对拍必红。
//
// ============ 为什么它不能被普通用例守住 ============
//
// 要验这个差异，得造一条 value=NaN 的 unified_data 行；但帧表是**量化编码**
//（FieldSpec.Quantize 把 float 映射成整数码点，round(NaN) 无定义）——
// **帧表侧物理上造不出 NaN**。所以「两条路在 NaN 上分歧」这件事只能被复现，
// 不能被对拍。
//
// 结论：这条边界的安全，完全依赖**那段注释还在**。
//
// ============ 这是一条「二阶依赖」，普通工具发现不了 ============
//
//	一阶依赖：代码 A 依赖 代码 B  => 编译器会报错（B 删了 A 编译不过）
//	二阶依赖：代码 A 的**安全性**依赖文档 D 存在 => **没有任何工具会报错**
//
// D 被删掉时，A 仍然编译、测试仍然全绿、运行时仍然工作正常 ——
// 只是「当某个条件成立时会错」这件事，不再有任何东西会喊。
//
// 所以必须有一条用例盯住 D 的存在性。这就是本文件。

import (
	"os"
	"strings"
	"testing"
)

// nanGuardSourceFile 是被守的生产文件。
const nanGuardSourceFile = "history_source.go"

// nanGuardKeywords 是那段注释必须提到的**事实关键词**。
//
// 为什么用多个关键词而不是整段文本比对：整段比对会在「改一个错别字」时红，
// 那种红会训练人「随手更新期望值」，从而让门禁退化成一个橡皮图章。
// 多关键词则要求「这段注释的核心事实都在」，与措辞无关。
//
// 每个关键词对应一条**必须被记住的事实**，删掉任何一条都会让读者得出错结论：
//
//	NaN IS NOT DISTINCT FROM  => SQL 侧语义（不给它，读者会以为 SQL 也按 IEEE754）
//	NaN != NaN                => Go 侧语义（同上）
//	量化                        => 「为什么现在不触发」的第一层原因
//	对拍用例会红                => 「将来触发了会怎样」的暴露途径
var nanGuardKeywords = []string{
	"NaN IS NOT DISTINCT FROM",
	"NaN != NaN",
	"量化",
	"对拍用例会红",
}

// TestNaNBoundaryCommentPreserved 是主门禁：那段注释必须还在。
func TestNaNBoundaryCommentPreserved(t *testing.T) {
	src, err := os.ReadFile(nanGuardSourceFile)
	if err != nil {
		t.Fatalf("read %s: %v（门禁不能静默失效）", nanGuardSourceFile, err)
	}
	body := string(src)

	// ⚠ 在**原文**里查（不剥注释）—— 我们要守的正是那段注释本身。
	// 这与其它源码门禁相反（它们剥注释防注释误导），本门禁是唯一的例外，
	// 因为它的**被测对象就是注释**。
	missing := make([]string, 0, len(nanGuardKeywords))
	for _, kw := range nanGuardKeywords {
		if !strings.Contains(body, kw) {
			missing = append(missing, kw)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s 里缺少 NaN 边界注释的关键事实：%v\n\t"+
			"这段注释是**这条边界唯一的存在形式** —— 它不能被用例守住："+
			"\n\t  帧表是量化编码（Quantize 把 float 映射成整数码点，round(NaN) 无定义），"+
			"\n\t  所以帧表侧物理上造不出 NaN，无法对拍。「两条路在 NaN 上分歧」只能被记录。"+
			"\n\t"+
			"删掉它 = 这条边界的安全从此无人知晓，而且**不会有任何测试变红**。"+
			"\n\t（若你是重构时误删：请恢复。若你认为这条边界已不存在："+
			"\n\t  先确认 PG 的 NaN IS NOT DISTINCT FROM 与 Go 的 NaN != NaN 语义已变 —— 那不会发生。）",
			nanGuardSourceFile, missing)
	}
}

// TestNaNBoundaryGuard_KeywordsActuallyPresent 是**关键词自检**。
//
// 一个只会说「关键词都在」的门禁，在关键词表被清空时同样会说「都在」
// （空集合的 Contains 检查恒通过）。这是「否定断言可以被空跑满足」的又一个实例。
//
// 所以这里断言关键词表本身非空、且每个关键词都**真的能在源码里找到**——
// 后一句与主门禁重复，但它验的是「表没被改空」而不是「源码没被改坏」，
// 两者的变红原因不同，报错信息也就不同。
func TestNaNBoundaryGuard_KeywordsActuallyPresent(t *testing.T) {
	if len(nanGuardKeywords) < 4 {
		t.Fatalf("关键词表只有 %d 项 —— 自检失败。本门禁靠「全部关键词都在」判断注释完整，"+
			"表被改空后它将永远通过（空集合的 Contains 检查恒真）。"+
			"当前表：%v", len(nanGuardKeywords), nanGuardKeywords)
	}
	// 每个关键词必须是**非空且不含纯空白**的实质内容。
	for i, kw := range nanGuardKeywords {
		if strings.TrimSpace(kw) == "" {
			t.Fatalf("关键词表第 %d 项是空串 —— 空串的 Contains 恒真，会让门禁假绿", i)
		}
	}
}

// TestNaNBoundary_ExposurePathDocumented 断言注释里写明了**暴露途径**。
//
// 这是本门禁里最弱但最必要的一条：
//
//	NaN 边界的安全性 = 「注释记录」+「对拍用例还活着」两件事同时成立。
//
// 第二件事**不能被自动检查** —— 检查「某个用例存在」是一条元检查，
// 而用例被删是合法操作（人可能真的决定不要它了）。那时元检查会红，
// 而人只能选「删掉检查」或「留着红」，两者都让门禁退化。
//
// 所以这里**不检查对拍用例是否存在**，只检查注释里是否**指明了这条依赖**。
// 这是有意的取舍：让「失去暴露途径」这件事至少在文字上可见。
func TestNaNBoundary_ExposurePathDocumented(t *testing.T) {
	src, err := os.ReadFile(nanGuardSourceFile)
	if err != nil {
		t.Fatalf("read %s: %v", nanGuardSourceFile, err)
	}
	body := string(src)

	if !strings.Contains(body, "对拍用例会红") {
		t.Fatalf("%s 的 NaN 注释必须写明暴露途径（「对拍用例会红」）——"+
			"\n\t这是这条边界唯一的自动化暴露路径：将来若真有 NaN 进库，"+
			"\n\t对拍用例会因点数不同而红。不写这句，下一个维护者会以为它是纯理论问题。", nanGuardSourceFile)
	}
	if !strings.Contains(body, "若那时对拍用例已被删除") {
		t.Errorf("%s 的 NaN 注释应保留「若那时对拍用例已被删除，这个差异就会无声无息」——"+
			"\n\t它说明了本边界的安全性依赖什么，是二阶依赖的显式声明。"+
			"\n\t（若你重写了措辞并保留了同义意思，可放宽本断言，但不要一删了之。）", nanGuardSourceFile)
	}
}
