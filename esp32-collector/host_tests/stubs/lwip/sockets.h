/**
 * @file sockets.h
 * @brief 宿主测试用的 lwip/sockets.h 桩（D-24）
 *
 * 为什么需要它：ehome_tcp.c（514 行，本仓最大的未覆盖文件）直接使用
 * lwip 的 socket API。宿主上没有 lwip ⇒ 给出这个最小桩。
 *
 * 设计要点：本桩【只】声明接口，不定义行为 ——
 * 行为由每个测试自己提供（例如用可控的假 send() 验证 D-10 的部分写处理）。
 * 这样"测的是什么"完全由测试决定，桩不会偷偷替被测代码做决定。
 */
#ifndef EHOME_HOST_STUB_LWIP_SOCKETS_H
#define EHOME_HOST_STUB_LWIP_SOCKETS_H

#include <stddef.h>
#include <stdint.h>
#include <sys/types.h>

/* 必须与 glibc 的 __socklen_t 一致（unsigned int）：
 * ehome_tcp.c 同时包含 <unistd.h>，而 glibc 在那里也 typedef 了 socklen_t。
 * 若这里写成 int，C11 的"重复 typedef 需同类型"规则会被违反 ⇒ 编译错误。 */
typedef unsigned int socklen_t;

#ifndef AF_INET
#define AF_INET 2
#endif
#ifndef SOCK_STREAM
#define SOCK_STREAM 1
#endif
#ifndef IPPROTO_TCP
#define IPPROTO_TCP 6
#endif
#ifndef SOL_SOCKET
#define SOL_SOCKET 1
#endif
#ifndef SO_REUSEADDR
#define SO_REUSEADDR 2
#endif
#ifndef INADDR_ANY
#define INADDR_ANY 0u
#endif

struct in_addr { uint32_t s_addr; };

struct sockaddr {
    uint8_t sa_len;
    uint8_t sa_family;
    char    sa_data[14];
};

struct sockaddr_in {
    uint8_t        sin_len;
    uint8_t        sin_family;
    uint16_t       sin_port;
    struct in_addr sin_addr;
    char           sin_zero[8];
};

int     socket(int domain, int type, int protocol);
int     bind(int s, const struct sockaddr *name, socklen_t namelen);
int     listen(int s, int backlog);
int     accept(int s, struct sockaddr *addr, socklen_t *addrlen);
int     setsockopt(int s, int level, int optname, const void *optval, socklen_t optlen);
int     close(int s);

/* 这两个是【被测对象】要拦截的 —— 测试提供实现。 */
ssize_t send(int s, const void *data, size_t size, int flags);
ssize_t recv(int s, void *mem, size_t len, int flags);

uint16_t htons(uint16_t v);
uint16_t ntohs(uint16_t v);
uint32_t htonl(uint32_t v);
uint32_t ntohl(uint32_t v);
char    *inet_ntoa(struct in_addr addr);

#endif /* EHOME_HOST_STUB_LWIP_SOCKETS_H */
