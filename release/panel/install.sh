#!/usr/bin/env bash
set -Eeuo pipefail

REPOSITORY="${SHABI_REPOSITORY:-ballardmandy69/sing-box}"
VERSION="${SHABI_VERSION:-latest}"
INSTALL_DIR="/opt/shabi"
STATE_DIR="/var/lib/shabi"
BIN_PATH="${INSTALL_DIR}/shabi"
temporary_dir=""

cleanup() {
  if [[ -n "${temporary_dir}" && -d "${temporary_dir}" ]]; then
    rm -rf -- "${temporary_dir}"
  fi
}
trap cleanup EXIT

action="install"
purge="false"
case "${1:-}" in
  install|update|uninstall)
    action="$1"
    shift
    ;;
esac
if [[ "${1:-}" == "--purge" ]]; then
  purge="true"
fi

if [[ "${EUID}" -ne 0 ]]; then
  echo "This installer must run as root." >&2
  exit 1
fi

install_packages() {
  local missing=()
  for command_name in curl openssl sha256sum systemctl; do
    command -v "${command_name}" >/dev/null 2>&1 || missing+=("${command_name}")
  done
  if [[ "${#missing[@]}" -eq 0 ]]; then
    return
  fi
  if ! command -v apt-get >/dev/null 2>&1; then
    echo "Missing commands: ${missing[*]}. Install them and run this script again." >&2
    exit 1
  fi
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates coreutils curl openssl
}

write_units() {
  cat >/etc/systemd/system/sb@.service <<'UNIT'
[Unit]
Description=AnyTLS panel server (%i, shared config.json)
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/shabi
ExecStart=/opt/shabi/shabi server --disable-color -c /opt/shabi/config.json -s /opt/shabi/%i.yml --state-directory /var/lib/shabi/%i
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=3s
TimeoutStopSec=15s
KillMode=mixed
LimitNOFILE=1048576
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT

  cat >/etc/systemd/system/sbc@.service <<'UNIT'
[Unit]
Description=AnyTLS panel server (%i, per-instance JSON)
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/shabi
ExecStart=/opt/shabi/shabi server --disable-color -c /opt/shabi/%i.json -s /opt/shabi/%i.yml --state-directory /var/lib/shabi/%i
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=3s
TimeoutStopSec=15s
KillMode=mixed
LimitNOFILE=1048576
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT
}

write_default_config() {
  if [[ ! -e "${INSTALL_DIR}/config.json" ]]; then
    cat >"${INSTALL_DIR}/config.json" <<'JSON'
{
  "log": {
    "level": "info",
    "timestamp": true
  },
  "dns": {
    "servers": [
      {
        "type": "local",
        "tag": "local"
      }
    ],
    "final": "local"
  },
  "inbounds": [],
  "outbounds": [
    {
      "type": "direct",
      "tag": "DefaultOut"
    },
    {
      "type": "block",
      "tag": "block"
    }
  ],
  "route": {
    "final": "DefaultOut"
  }
}
JSON
    chmod 600 "${INSTALL_DIR}/config.json"
  fi

  if [[ ! -e "${INSTALL_DIR}/a.yml.example" ]]; then
    cat >"${INSTALL_DIR}/a.yml.example" <<'YAML'
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
    # Fallback: "127.0.0.1:8080"
    # FallbackForALPN:
    #   h2: "127.0.0.1:8081"
    #   http/1.1: "127.0.0.1:8080"
    # FallbackForServerName:
    #   example.com: "127.0.0.1:8080"
YAML
    chmod 600 "${INSTALL_DIR}/a.yml.example"
  fi
}

create_certificate() {
  if [[ -s "${INSTALL_DIR}/zq.crt" && -s "${INSTALL_DIR}/zq.key" ]]; then
    return
  fi
  local common_name="${SHABI_CERT_CN:-www.cloudflare.com}"
  openssl req -x509 -newkey rsa:2048 -sha256 -nodes -days 3650 \
    -keyout "${INSTALL_DIR}/zq.key" \
    -out "${INSTALL_DIR}/zq.crt" \
    -subj "/CN=${common_name}" \
    -addext "subjectAltName=DNS:${common_name}"
  cat "${INSTALL_DIR}/zq.crt" "${INSTALL_DIR}/zq.key" >"${INSTALL_DIR}/zq.pem"
  chmod 600 "${INSTALL_DIR}/zq.key" "${INSTALL_DIR}/zq.pem"
  chmod 644 "${INSTALL_DIR}/zq.crt"
}

download_binary() {
  local machine architecture asset base_url expected
  machine="$(uname -m)"
  case "${machine}" in
    x86_64|amd64) architecture="amd64" ;;
    aarch64|arm64) architecture="arm64" ;;
    *)
      echo "Unsupported architecture: ${machine}" >&2
      exit 1
      ;;
  esac
  asset="shabi-linux-${architecture}"
  if [[ "${VERSION}" == "latest" ]]; then
    base_url="https://github.com/${REPOSITORY}/releases/latest/download"
  else
    base_url="https://github.com/${REPOSITORY}/releases/download/${VERSION}"
  fi
  temporary_dir="$(mktemp -d)"
  curl -fL --retry 3 --connect-timeout 15 "${base_url}/${asset}" -o "${temporary_dir}/${asset}"
  curl -fL --retry 3 --connect-timeout 15 "${base_url}/checksums.txt" -o "${temporary_dir}/checksums.txt"
  expected="$(awk -v name="${asset}" '$2 == name || $2 == "*" name {print $1; exit}' "${temporary_dir}/checksums.txt")"
  if [[ -z "${expected}" ]]; then
    echo "No checksum found for ${asset}." >&2
    exit 1
  fi
  printf '%s  %s\n' "${expected}" "${temporary_dir}/${asset}" | sha256sum -c -
  if [[ -e "${BIN_PATH}" ]]; then
    mkdir -p "${INSTALL_DIR}/backup"
    cp -a "${BIN_PATH}" "${INSTALL_DIR}/backup/shabi.$(date +%Y%m%d%H%M%S)"
  fi
  install -m 755 "${temporary_dir}/${asset}" "${BIN_PATH}"
  ln -sfn "${BIN_PATH}" /usr/local/bin/shabi
}

uninstall_service() {
  systemctl stop 'sb@*.service' 'sbc@*.service' >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/sb@.service /etc/systemd/system/sbc@.service
  rm -f /usr/local/bin/shabi
  systemctl daemon-reload
  if [[ "${purge}" == "true" ]]; then
    rm -rf -- "${INSTALL_DIR}" "${STATE_DIR}"
    echo "Removed binary, services, configuration, certificates, and state."
  else
    rm -f "${BIN_PATH}"
    echo "Removed binary and services. Configuration remains in ${INSTALL_DIR}."
  fi
}

if [[ "${action}" == "uninstall" ]]; then
  uninstall_service
  exit 0
fi

install_packages
install -d -m 755 "${INSTALL_DIR}"
install -d -m 700 "${STATE_DIR}"
download_binary
write_units
write_default_config
create_certificate
systemctl daemon-reload

echo
echo "Installed AnyTLS panel server to ${BIN_PATH}."
echo "Example configuration: ${INSTALL_DIR}/a.yml.example"
echo "Shared JSON mode:       systemctl enable --now sb@a"
echo "Per-instance JSON mode: systemctl enable --now sbc@a"
echo "Validate first:         shabi server --check -c ${INSTALL_DIR}/config.json -s ${INSTALL_DIR}/a.yml"
echo
echo "Certificate public-key SHA-256:"
openssl x509 -in "${INSTALL_DIR}/zq.crt" -pubkey -noout |
  openssl pkey -pubin -outform DER 2>/dev/null |
  openssl dgst -sha256 | awk '{print $2}'
