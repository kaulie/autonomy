#!/usr/bin/env bash
# Listen-address helpers for scripts/start.sh.
#
# Port is never taken from backend/.env (SERVICE_PORT > PORT > 4300): a leftover
# AUTONOMY_HTTP_ADDR must not pin the service to an old contract port.
#
# Host is the remote-server knob:
#   AUTONOMY_HTTP_HOST (env / .env) > SERVICE_HOST (platform) > 127.0.0.1
# A local-platform deploy stays on loopback. A remote server that should accept
# traffic from other machines sets AUTONOMY_HTTP_HOST=0.0.0.0 (or a NIC address).
#
# This file is sourced; it is also executable so the unit test can call one
# function at a time without starting the runtime.

compose_http_addr() {
  local host="$1" port="$2"
  if [ -z "${host}" ]; then
    echo "listen host is empty" >&2
    return 1
  fi
  case "${host}" in
    *$'\n'*|*$'\r'*|*' '*)
      echo "listen host is not a single token: ${host}" >&2
      return 1
      ;;
  esac
  case "${port}" in
    *[!0-9]*|"")
      echo "listen port must be 1-65535: ${port}" >&2
      return 1
      ;;
  esac
  if [ "${port}" -lt 1 ] || [ "${port}" -gt 65535 ]; then
    echo "listen port out of range: ${port}" >&2
    return 1
  fi
  case "${host}" in
    \[*\]) echo "${host}:${port}" ;;
    *:*)   echo "[${host}]:${port}" ;;
    *)     echo "${host}:${port}" ;;
  esac
}

resolve_listen_host() {
  if [ -n "${AUTONOMY_HTTP_HOST:-}" ]; then
    printf '%s\n' "${AUTONOMY_HTTP_HOST}"
    return
  fi
  if [ -n "${SERVICE_HOST:-}" ]; then
    printf '%s\n' "${SERVICE_HOST}"
    return
  fi
  printf '%s\n' "127.0.0.1"
}

# 0.0.0.0 / :: mean "all interfaces". curl-ing those is not portable; probe loopback.
health_probe_host() {
  local host="$1"
  case "${host}" in
    0.0.0.0|::|\[::\]) echo "127.0.0.1" ;;
    \[*\]) echo "${host}" ;;
    *:*) echo "[${host}]" ;;
    *) echo "${host}" ;;
  esac
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  cmd="${1:-}"
  shift || true
  case "${cmd}" in
    compose) compose_http_addr "$@" ;;
    host) resolve_listen_host ;;
    probe) health_probe_host "$@" ;;
    *)
      echo "usage: $0 compose HOST PORT | host | probe HOST" >&2
      exit 2
      ;;
  esac
fi
