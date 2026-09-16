"""route-coverage: 对账 router 的业务子路由与 e2e 受审清单 ROUTES。

为什么必须用 python：业务子路由是相对路径（path: "channel"），
而 ROUTES 里是绝对路径（path: "/channel"）；grep 单条正则必然漏。
另外必须排除顶层绝对路径（/login、/403、/dev/*）—— 它们不是布局下的业务页。
"""
import re, io, sys

router = io.open('src/router/index.ts', encoding='utf-8').read()
fix = io.open('e2e/helpers/uiux-fixtures.ts', encoding='utf-8').read()

RE_REL = re.compile("path: '([a-z][a-zA-Z0-9_-]*)'")
RE_ABS = re.compile("path: '/([a-zA-Z0-9_-]+)'")

rel = sorted(set(RE_REL.findall(router)))
routes = sorted(set(RE_ABS.findall(fix)))

missing = [r for r in rel if r not in routes]
extra = [r for r in routes if r not in rel]

print("router 业务子路由 %d 个: %s" % (len(rel), rel))
print("e2e ROUTES      %d 个: %s" % (len(routes), routes))
print("缺失（router 有、ROUTES 无）: %s" % (missing,))
print("多余（ROUTES 有、router 无）: %s" % (extra,))
sys.exit(1 if missing else 0)
