#!/usr/bin/env python3
"""实测实验：TCP 是否保留应用层 write 边界？（决定应用层分片在 TCP 上有无收益）

## 为什么需要这个实验
设计 §5.1 的帧格式含 frag_off/frag_len/MORE，即应用层分片。
它隐含一个前提：收方一次 recv 能拿到一片。若该前提不成立，
则收方无论如何都要自己累积与解析，应用层分片就只剩下字段与状态的开销。

本脚本用本机回环实测这个前提。要证明前提不成立，
只需一次边界被合并的观测 —— 不需要每次都发生。

## 实测结果（2026-10-06，本机 127.0.0.1）

  5 次 write，每次 64 B，无延迟      writes=5 -> recv=5  [64,64,64,64,64]    边界保留
  5 次 write，每次 64 B，间隔 20 ms  writes=5 -> recv=5  [64,64,64,64,64]    边界保留
  3 次 write，每次 1024 B，无延迟     writes=3 -> recv=2  [2048,1024]         前两片被合并
  8 次 write，每次 128 B，间隔 5 ms   writes=8 -> recv=8  [128 x8]            边界保留

关键观测：3 x 1024 B 被合并成 1 次 2048 B 的 recv。
1024 B 正是设计 §5.2 规定的 frag_len 硬上界 ——
即按最大片发 3 片完全可能到达为 2 次 recv，其中第一次含 2 片。

=> TCP 不保留应用层 write 边界（这是 TCP 的定义性质，本实验只是在本机复现）。
=> 收方必须自行累积与解析，应用层分片换不来任何一次 recv = 一片的保证。

## 复跑

  python3 tools/verify_tcp_is_stream.py

注意：这是回环。真实网络（Nagle、拥塞窗口、MTU 分片、中间设备）只会让
合并与拆分更不可预测，不会更可预测。
"""
import socket
import threading
import time
import sys


def run(n_writes, delay, payload_len):
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", 0))
    srv.listen(1)
    port = srv.getsockname()[1]
    result = {}

    def receiver():
        conn, _ = srv.accept()
        conn.settimeout(1.0)
        chunks = []
        try:
            while True:
                b = conn.recv(65536)
                if not b:
                    break
                chunks.append(len(b))
        except socket.timeout:
            pass
        result['chunks'] = chunks
        conn.close()

    t = threading.Thread(target=receiver)
    t.start()
    cli = socket.create_connection(("127.0.0.1", port))
    for _ in range(n_writes):
        cli.sendall(b"x" * payload_len)
        if delay:
            time.sleep(delay)
    cli.shutdown(socket.SHUT_WR)
    cli.close()
    t.join(timeout=3)
    srv.close()
    return result.get('chunks', [])


def main():
    print("=== TCP 是否保留应用层 write 边界（本机回环实测）===")
    cases = [
        (5, 0.0,   64,   "5 x 64 B, no delay"),
        (5, 0.02,  64,   "5 x 64 B, 20 ms gap"),
        (3, 0.0,   1024, "3 x 1024 B, no delay"),
        (8, 0.005, 128,  "8 x 128 B, 5 ms gap"),
    ]
    merged = False
    for n, d, ln, label in cases:
        chunks = run(n, d, ln)
        flag = ""
        if len(chunks) != n:
            flag = "   <== boundaries merged/split"
            merged = True
        print("  %-28s writes=%d -> recv=%d  %s%s" % (label, n, len(chunks), chunks[:8], flag))

    print("")
    print("判读：只要出现一次边界被合并，就说明 TCP 不提供一次 recv = 一次 write 的保证。")
    if merged:
        print("结论：本机实测到边界被合并 => 应用层分片在 TCP 上换不来定界能力。")
    else:
        print("本次未观测到合并 —— 但这不证明保证存在（本实验只用于反证）。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
