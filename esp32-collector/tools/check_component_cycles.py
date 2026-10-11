#!/usr/bin/env python3
"""D-14 门禁：组件依赖图必须无环。

为什么需要它：本仓曾有且仅有一个组件依赖环 ——
    msg_handler <-> bus_worker
它【能跑】：ESP-IDF 的 component manager 会记录环并按字典序重复解析静态库
（build.cmake:399 明确处理）。所以编译器不会告诉你它存在，
它只会让"这两个组件无法单独构建/测试"，并在将来接线时反复绊人。

2026-10-06 已通过把上报统计量搬到中立组件 report_stats 破除。

判据：解析每个 components/*/CMakeLists.txt 里 idf_component_register 的
REQUIRES / PRIV_REQUIRES，构图后做环检测（DFS）。

退出码：0 = 无环；1 = 发现环；2 = 无法解析。
"""
import os
import re
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)
COMPONENTS = os.path.join(ROOT, "components")

REGISTER_RE = re.compile(r"idf_component_register\s*\((.*?)\)\s*$", re.S | re.M)
REQ_RE = re.compile(r"(?:PRIV_)?REQUIRES\s+([^)\n]*(?:\n\s+[^)\n]*)*)")


def parse_requires(text):
    """从 idf_component_register(...) 里取出所有 REQUIRES 目标名。"""
    names = []
    m = REGISTER_RE.search(text)
    body = m.group(1) if m else text
    for rm in REQ_RE.finditer(body):
        chunk = rm.group(1)
        # 去掉注释与引号，再按空白切分
        chunk = re.sub(r"#[^\n]*", " ", chunk)
        for tok in chunk.replace('"', " ").split():
            if tok and not tok.startswith("LDFRAGMENTS") and tok != "REQUIRES":
                names.append(tok)
    return names


def build_graph():
    graph = {}
    for name in sorted(os.listdir(COMPONENTS)):
        cdir = os.path.join(COMPONENTS, name)
        cm = os.path.join(cdir, "CMakeLists.txt")
        if not os.path.isdir(cdir) or not os.path.isfile(cm):
            continue
        with open(cm, encoding="utf-8", errors="replace") as fh:
            graph[name] = parse_requires(fh.read())
    # 只保留指向真实组件的边
    for k in graph:
        graph[k] = [d for d in graph[k] if d in graph]
    return graph


def find_cycles(graph):
    WHITE, GREY, BLACK = 0, 1, 2
    color = {n: WHITE for n in graph}
    stack = []
    cycles = []

    def dfs(node):
        color[node] = GREY
        stack.append(node)
        for dep in graph.get(node, []):
            if color.get(dep, BLACK) == GREY:
                i = stack.index(dep)
                cycles.append(stack[i:] + [dep])
            elif color.get(dep, BLACK) == WHITE:
                dfs(dep)
        stack.pop()
        color[node] = BLACK

    for n in sorted(graph):
        if color[n] == WHITE:
            dfs(n)
    return cycles


def main():
    graph = build_graph()
    if not graph:
        print("FAIL 未解析到任何组件（路径或格式变了，门禁需更新）")
        return 2
    cycles = find_cycles(graph)
    print("解析到 %d 个组件，%d 条依赖边" %
          (len(graph), sum(len(v) for v in graph.values())))
    if cycles:
        print("FAIL 存在组件依赖环（%d 个）：" % len(cycles))
        for c in cycles:
            print("   " + " -> ".join(c))
        print("   环会让组件无法单独构建/测试；请把共享部分抽到中立组件。")
        return 1
    print("PASS 组件依赖图无环")
    return 0


if __name__ == "__main__":
    sys.exit(main())
