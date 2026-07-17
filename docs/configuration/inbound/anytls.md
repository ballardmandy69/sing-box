---
icon: material/new-box
---

!!! question "Since sing-box 1.12.0"

### Structure

```json
{
  "type": "anytls",
  "tag": "anytls-in",

  ... // Listen Fields

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

### Listen Fields

See [Listen Fields](/configuration/shared/listen/) for details.

This fork keeps the inbound parser AnyTLS-only. Fallback traffic is forwarded
as opaque plaintext and is not parsed as Trojan, VLESS, or another proxy
protocol.

### Fields

#### users

==Required==

AnyTLS users.

#### padding_scheme

AnyTLS padding scheme line array.

Default padding scheme:

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

Enables AnyTLS padding on server-to-client control frames.

Enabled by default in this fork. Set to `false` only when compatibility with an
older implementation requires client-only padding.

#### authentication_timeout

Maximum time to wait for the 32-byte AnyTLS authentication hash before treating
a partial connection as fallback traffic.

Defaults to `5s`.

Non-HTTP authentication failures are held until this connection-specific
deadline before fallback, so the 32-byte authentication boundary does not
produce an immediate-response timing threshold. Recognized HTTP/1.x requests
and the HTTP/2 client preface are forwarded immediately.

#### authentication_timeout_jitter

Random variation applied to `authentication_timeout` for each connection.

Defaults to `2s` and is capped at half of `authentication_timeout`.

#### fallback

Default fallback server. It receives the original plaintext bytes after TLS has
been terminated by this inbound.

#### fallback_for_alpn

Fallback server configuration for negotiated ALPN values.

When configured, a non-empty ALPN value not present in this table is rejected.
Every key must also appear in `tls.alpn`, or configuration loading fails.
The `h2` destination must accept HTTP/2 cleartext prior knowledge (h2c), since
TLS has already been terminated. An ordinary cleartext HTTP server is suitable
for `http/1.1`.

#### fallback_for_server_name

Fallback server configuration for exact, case-insensitive TLS server names.

When configured, an unknown SNI is rejected. If both SNI and ALPN maps are
configured, SNI acts as an allowlist and the ALPN map selects the final
destination.

#### tls

TLS configuration, see [TLS](/configuration/shared/tls/#inbound).

Use a valid certificate for the advertised SNI and advertise only protocols
that the corresponding fallback backend actually serves. In particular,
advertising `h2` requires a working h2c fallback.

The built-in TLS server still uses Go's TLS implementation and does not emulate
a different server TLS fingerprint. To replace that behavior, terminate TLS in
an external stream proxy and forward the decrypted TCP stream to an AnyTLS
inbound with TLS disabled. In that layout, enforce SNI at the external proxy and
use the default fallback here; SNI and ALPN metadata are not preserved across a
plain TCP handoff.

For example, an HAProxy frontend can terminate TLS with OpenSSL and preserve the
decrypted byte stream:

```haproxy
frontend anytls_tls
  mode tcp
  bind :443 ssl crt /etc/haproxy/certs/example.com.pem alpn h2,http/1.1 strict-sni
  default_backend anytls_plain

backend anytls_plain
  mode tcp
  server anytls 127.0.0.1:8443
```

In this layout, listen on `127.0.0.1:8443` with the sing-box inbound `tls` field
omitted. Configure only `fallback` in sing-box and point it to a cleartext web
gateway that accepts both HTTP/1.1 and h2c on the same listener. HAProxy's PEM
file must contain the certificate and private key. See the
[HAProxy bind options](https://www.haproxy.com/documentation/haproxy-configuration-manual/latest/#5.1-alpn)
for ALPN and `strict-sni` behavior.
