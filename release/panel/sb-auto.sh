#!/usr/bin/env bash
set -Eeuo pipefail

if [[ "$#" -lt 2 ]]; then
  echo "Usage: $0 BASE_JSON_URL SERVER_YAML_URL" >&2
  exit 1
fi

base_json_url="$1"
server_yaml_url="$2"
instance="${INSTANCE:-a}"
install_dir="/opt/shabi"
installer_url="${SHABI_INSTALLER_URL:-https://github.com/ballardmandy69/sing-box/releases/latest/download/install.sh}"

if [[ "${NO_INSTALL:-0}" != "1" ]]; then
  bash <(curl -fLSs "${installer_url}") install
fi

install -d -m 755 "${install_dir}"
curl -fLSs "${base_json_url}" -o "${install_dir}/${instance}.json"
curl -fLSs "${server_yaml_url}" -o "${install_dir}/${instance}.yml"
chmod 600 "${install_dir}/${instance}.json" "${install_dir}/${instance}.yml"
systemctl enable --now "sbc@${instance}"
systemctl --no-pager --full status "sbc@${instance}"
