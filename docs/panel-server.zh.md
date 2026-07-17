# AnyTLS 面板服务模式

此分支提供 `server` 命令，用同一个二进制读取原后端风格的
`config.json/a.json + server.yml/a.yml`。服务端仅创建 AnyTLS 入站，不会创建
Trojan、VLESS 或三协议 fallback。

## 安装

正式 Release 发布后可使用：

```bash
bash <(curl -fLSs https://github.com/ballardmandy69/sing-box/releases/latest/download/install.sh) install
```

默认目录和文件：

```text
/opt/shabi/shabi
/opt/shabi/config.json
/opt/shabi/a.yml
/opt/shabi/zq.crt
/opt/shabi/zq.key
/var/lib/shabi/a/
```

安装器不会覆盖已有 JSON、YAML 或证书。首次安装生成的自签证书仅用于快速启动；
商用部署应替换为与节点域名匹配的有效证书。

## systemd 兼容

`sb@a` 读取共享的 `/opt/shabi/config.json` 和 `/opt/shabi/a.yml`：

```bash
systemctl enable --now sb@a
systemctl reload sb@a
systemctl restart sb@a
journalctl -u sb@a -f
```

`sbc@a` 读取 `/opt/shabi/a.json` 和 `/opt/shabi/a.yml`：

```bash
systemctl enable --now sbc@a
systemctl reload sbc@a
journalctl -u sbc@a -f
```

启动前检查面板连接、用户和生成配置：

```bash
/opt/shabi/shabi server --check \
  -c /opt/shabi/config.json \
  -s /opt/shabi/a.yml \
  --state-directory /var/lib/shabi/a
```

## YAML 格式

基础格式与提供的示例一致：

```yaml
DisableAccessLog: true
IPLimit: 5
RateLimit: 100
ConnectionCleanupIntervalSec: 120
HotUserCacheSec: 600

Nodes:
  - Type: UniProxy
    UpdateInterval: "60s"
    ApiConfig:
      PanelTag: "atls-1"
      ApiHost: https://panel.example.com
      ApiKey: replace-with-server-token
      NodeType: AnyTLS
      NodeID: 111
      Timeout: 10
      ReportAlive: true
    CertConfig:
      CertFile: "./zq.crt"
      KeyFile: "./zq.key"
    ALPN:
      - h2
      - http/1.1
    ServerPadding: true
```

支持 Xboard/V2Board UniProxy V1 接口：

```text
/api/v1/server/UniProxy/config
/api/v1/server/UniProxy/user
/api/v1/server/UniProxy/push
/api/v1/server/UniProxy/alive
```

请求参数为 `token`、`node_id` 和 `node_type=anytls`。用户密码读取面板返回的
`uuid`，流量按用户 ID 回传；`ReportAlive: true` 时也会上报当前连接 IP。
面板暂时不可用时，服务可使用 `/var/lib/shabi/<实例>/panel-cache.json` 启动。

## 回落扩展

真实网站后端仍在同一个 YAML 节点中配置：

```yaml
    Fallback: "127.0.0.1:8080"
    FallbackForALPN:
      h2: "127.0.0.1:8081"
      http/1.1: "127.0.0.1:8080"
    FallbackForServerName:
      example.com: "127.0.0.1:8080"
```

`h2` 目标必须是真正支持 h2c 的后端。证书、SNI 和网站域名应保持一致。

## 兼容边界

- 只运行 `Type: UniProxy` 且 `NodeType: AnyTLS` 的节点；示例文件中的其他协议会明确跳过。
- `AcceptAnyTLS` 和 `FallbackService` 不会启用 Trojan/VLESS 协议 fallback。
- `ProxyProtocol: true` 当前会拒绝启动，避免误以为已正确解析来源地址。
- `IPLimit`、`RateLimit`、`ConnectionLimit`、用户 `speed_limit` 当前只完成格式解析，尚未在核心中执行限制。
- 旧 JSON 的注释、旧 DNS `address` 写法和 `route.ip_on_demand` 可由兼容层转换。
