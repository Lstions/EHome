package automation

// testLocalZone 返回与 time.Local 同义的 IANA 时区名，供 time_window 夹具使用。
//
// 2026-11-18 D7 起 time_window 必须带规则时区（缺失即 fail-closed 跳过），
// 而这些用例用 time.Local 构造时刻；两者必须同一时区，否则用例会在 UTC
// 与东八区之间漂 8 小时（在本机绿、在 CI 红，或反过来）。
// 取不到 IANA 名时退回 "UTC" —— 此时 time.Local 通常就是 UTC；若用户显式设了
// 非法的 TZ，用例会明确失败而不是静默漂移。
func testLocalZone() string {
	if name := localZoneName(); name != "" {
		return name
	}
	return "UTC"
}
