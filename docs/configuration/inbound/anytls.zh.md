---
icon: material/new-box
---

!!! question "自 sing-box 1.12.0 起"

### 结构

```json
{
  "type": "anytls",
  "tag": "anytls-in",

  ... // 监听字段

  "users": [
    {
      "name": "sekai",
      "password": "8JCsPssfgS8tiRwiMlhARg=="
    }
  ],
  "padding_scheme": [],
  "server_padding": true,
  "authentication_timeout": "5s",
  "authentication_timeout_jitter": "2s",
  "tls": {
    "enabled": true,
    "server_name": "example.com",
    "alpn": [
      "h2",
      "http/1.1"
    ],
    "certificate_path": "/etc/ssl/example.com/fullchain.pem",
    "key_path": "/etc/ssl/example.com/privkey.pem"
  },
  "fallback": {
    "server": "127.0.0.1",
    "server_port": 8080
  },
  "fallback_for_alpn": {
    "h2": {
      "server": "127.0.0.1",
      "server_port": 8082
    },
    "http/1.1": {
      "server": "127.0.0.1",
      "server_port": 8080
    }
  },
  "fallback_for_server_name": {
    "example.com": {
      "server": "127.0.0.1",
      "server_port": 8080
    }
  }
}
```

### 监听字段

参阅 [监听字段](/zh/configuration/shared/listen/)。

此 Fork 的入站解析器只识别 AnyTLS。回落流量会作为不透明明文直接转发，不会继续
尝试解析 Trojan、VLESS 或其他代理协议。

### 字段

#### users

==必填==

AnyTLS 用户。

#### padding_scheme

AnyTLS 填充方案行数组。

默认填充方案:

```json
[
  "stop=8",
  "0=30-30",
  "1=100-400",
  "2=400-500,c,500-1000,c,500-1000,c,500-1000,c,500-1000",
  "3=9-9,500-1000",
  "4=500-1000",
  "5=500-1000",
  "6=500-1000",
  "7=500-1000"
]
```

#### server_padding

为服务端发往客户端的 AnyTLS 控制帧启用填充。

此 Fork 默认启用。仅在兼容只支持客户端填充的旧实现时设为 `false`。

#### authentication_timeout

等待 32 字节 AnyTLS 认证哈希的最长时间；超时后，已收到的部分数据会按回落流量处理。

默认值为 `5s`。

非 HTTP 的认证失败会统一等待到该连接自己的随机认证截止时间后再回落，因此不会因
达到 32 字节而产生“立即响应”的时序阈值。已识别的 HTTP/1.x 请求和 HTTP/2 客户端
前言会立即转发。

#### authentication_timeout_jitter

为每条连接的 `authentication_timeout` 增加随机浮动。

默认值为 `2s`，并限制为不超过 `authentication_timeout` 的一半。

#### fallback

默认回落服务器。TLS 在本入站完成终止后，原始明文数据会完整转发到该服务器。

#### fallback_for_alpn

按 TLS 协商得到的 ALPN 指定回落服务器。

配置后，表中不存在的非空 ALPN 会被拒绝；每个键也必须出现在 `tls.alpn` 中，
否则配置加载失败。`h2` 目标必须支持 HTTP/2 明文先验模式（h2c），因为 TLS
已在本入站终止；`http/1.1` 目标可使用普通明文 HTTP 服务。

#### fallback_for_server_name

按 TLS SNI 精确匹配回落服务器，匹配时不区分大小写。

配置后，未知 SNI 会被拒绝。同时配置 SNI 和 ALPN 映射时，SNI 作为准入白名单，
ALPN 映射负责选择最终目标。

#### tls

TLS 配置, 参阅 [TLS](/zh/configuration/shared/tls/#入站)。

证书必须对声明的 SNI 有效，并且只声明回落后端真正支持的协议；声明 `h2` 时，
必须提供可工作的 h2c 回落。

内置 TLS 服务仍使用 Go 的 TLS 实现，不会模拟其他服务端 TLS 指纹。若需要替换
Go TLS 行为，应由外部流式代理终止 TLS，再把解密后的 TCP 流转发到关闭 TLS 的
AnyTLS 入站。在这种架构下，应在外部代理执行 SNI 策略，并在这里使用默认回落；
普通 TCP 转交不会保留 SNI 与 ALPN 元数据。

例如，可用 HAProxy 的 OpenSSL 前端终止 TLS，同时保留解密后的原始字节流：

```haproxy
frontend anytls_tls
  mode tcp
  bind :443 ssl crt /etc/haproxy/certs/example.com.pem alpn h2,http/1.1 strict-sni
  default_backend anytls_plain

backend anytls_plain
  mode tcp
  server anytls 127.0.0.1:8443
```

此时 sing-box 入站监听 `127.0.0.1:8443`，并省略 `tls` 字段。sing-box 中只配置
默认 `fallback`，指向能在同一明文监听端口同时接收 HTTP/1.1 与 h2c 的网站网关。
HAProxy 使用的 PEM 文件必须同时包含证书和私钥。ALPN 与 `strict-sni` 行为参阅
[HAProxy bind 选项](https://www.haproxy.com/documentation/haproxy-configuration-manual/latest/#5.1-alpn)。
