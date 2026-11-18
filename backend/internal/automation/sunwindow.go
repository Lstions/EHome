package automation

import (
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sun"
)

// sunEventCandidate 是「某一本地日的日出/日落 + 规则偏移」的候选触发。
//
// eventDate 是**事件所属的节点本地日**（不是 tick 的本地日）—— sunStates 用它
// 做「每场事件只触发一次」的判据。跨午夜偏移时两者不同：用 tick 日期会让同一场
// 事件在次日再触发一次，或用相邻日的事件顶替本场事件。
type sunEventCandidate struct {
	eventDate string    // "2006-01-02"（规则时区）
	eventAt   time.Time // 当日日出/日落时刻（规则时区）
	fireAt    time.Time // eventAt + 偏移
}

// sunCandidatesAround 返回 now 所在本地日及前后各一天的候选事件（按事件日期升序）。
//
// 为什么必须是三日（2026-11-18 D22）：偏移范围 ±720 分钟，足以把 fireAt 推到
// 前一天或后一天（日落 23:40 + 60 = 次日 00:40；日出 03:30 - 300 = 前一日 22:30）。
// 旧实现只取「now 当日那一场」事件：到了真正的 fireAt，now 已属于相邻本地日，
// 算出来的是再下一场事件 ⇒ now < fireAt 恒成立，规则**一次都不会触发**
// （实测：北京日出 -600 分钟 / 日落 +300 分钟均 0 次）。候选集让跨午夜与不跨午夜
// 走同一条边沿判定。
//
// 极昼/极夜日由 SunEvent 返回非 OK 状态，该日直接跳过；三日都无解时返回空集，
// 由调用方落 suppressed_no_solar_event 审计（不静默）。
//
// 缓存命中：三日 × 2 事件的键都在进程内 sun.Cache 里，跨日后由 pruneLocked 淘汰。
func (e *Evaluator) sunCandidatesAround(resolved models.SunLocation, now time.Time,
	kind sun.Kind, offset time.Duration) ([]sunEventCandidate, error) {
	loc := resolved.Location
	if loc == nil {
		return nil, sun.ErrNilLocation
	}
	out := make([]sunEventCandidate, 0, 3)
	for _, dayOffset := range [...]int{-1, 0, 1} {
		day := now
		if dayOffset != 0 {
			day = now.AddDate(0, 0, dayOffset)
		}
		eventAt, status, err := e.sunCache.Lookup(resolved.Latitude, resolved.Longitude, loc, day, kind)
		if err != nil {
			return nil, err
		}
		if status != sun.StatusOK {
			continue
		}
		out = append(out, sunEventCandidate{
			eventDate: day.In(loc).Format("2006-01-02"),
			eventAt:   eventAt,
			fireAt:    eventAt.Add(offset),
		})
	}
	return out, nil
}
