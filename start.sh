#!/bin/sh
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"

mover_fail() { printf '%s\n' "$*" >&2; exit 1; }

# Inspect the current configuration only: never edit interfaces or restart net.
mover_network_configured() {
  command -v ip >/dev/null 2>&1 || return 1
  for mover_family in -4 -6; do
    mover_routes=$(ip "$mover_family" route show default 2>/dev/null) || continue
    mover_devices=$(printf '%s\n' "$mover_routes" | awk '
      /^default / && !/ linkdown/ {
        for (i = 1; i < NF; i++) if ($i == "dev") print $(i + 1)
      }')
    for mover_device in $mover_devices; do
      mover_addresses=$(ip "$mover_family" addr show dev "$mover_device" up scope global 2>/dev/null) || continue
      case "$mover_addresses" in *NO-CARRIER*|*'state DOWN'*) continue ;; esac
      if printf '%s\n' "$mover_addresses" | grep -Eq '^[[:space:]]*inet6?[[:space:]]'; then
        return 0
      fi
    done
  done
  return 1
}

mover_recover_openrc() {
  # Bypass dependencies only for this exact known network parser failure.
  case "$mover_start_output" in
    *'could not parse /etc/network/interfaces'*) ;;
    *) mover_fail 'OpenRC could not start Docker. Check the service error above.' ;;
  esac
  case "$mover_start_output" in
    *'cannot start docker as networking would not start'*) ;;
    *) mover_fail 'Docker failed for a different reason; dependency recovery was not applied.' ;;
  esac
  mover_network_configured || mover_fail 'OpenRC networking failed and no active address with a default route was found. Check the host network configuration.'
  printf '%s\n' 'Network is already configured. Preparing sysfs/cgroups and starting Docker without the broken networking dependency.'
  for mover_service in sysfs cgroups; do
    rc-service "$mover_service" start || mover_fail "Could not start required service: $mover_service."
  done
  rc-service --nodeps docker start || mover_fail 'Docker still failed after preparing its filesystem dependencies.'
  printf '%s\n' 'Docker recovery is temporary; /etc/network/interfaces was not changed.'
}

command -v docker >/dev/null 2>&1 || mover_fail 'Install Docker first: apk add docker docker-cli-compose'
docker compose version >/dev/null 2>&1 || mover_fail 'Install the Docker Compose plugin: apk add docker-cli-compose'
case "${MOVER_BUILD:-0}:${MOVER_SKIP_BUILD:-0}" in
  0:0)
    MOVER_IMAGE=${MOVER_IMAGE:-ghcr.io/pokys/esxi-mover:latest}
    mover_compose_file=compose.image.yaml
    ;;
  1:0|0:1)
    MOVER_IMAGE=${MOVER_IMAGE:-esxi-mover:local}
    mover_compose_file=compose.yaml
    ;;
  *) mover_fail 'Use MOVER_BUILD=1 for a source build or MOVER_SKIP_BUILD=1 for an offline image; do not combine them.' ;;
esac
export MOVER_IMAGE
mover_compose() { docker compose -f "$mover_compose_file" "$@"; }

if ! docker info >/dev/null 2>&1; then
  if command -v rc-service >/dev/null 2>&1; then
    if mover_start_output=$(LC_ALL=C rc-service docker start 2>&1); then
      printf '%s\n' "$mover_start_output"
    else
      printf '%s\n' "$mover_start_output" >&2
      mover_recover_openrc
    fi
    mover_wait=0
    until docker info >/dev/null 2>&1; do
      if [ "$mover_wait" -ge 30 ]; then
        printf '%s\n' 'Docker did not become ready within 30 seconds. Check rc-service docker status and /var/log/docker.log.' >&2
        exit 1
      fi
      sleep 1
      mover_wait=$((mover_wait + 1))
    done
  else
    mover_fail 'Start the Docker daemon, then run this script again.'
  fi
fi
if [ -z "${MOVER_APPLIANCE_UUID:-}" ] && [ -r /sys/class/dmi/id/product_uuid ]; then
  MOVER_APPLIANCE_UUID=$(cat /sys/class/dmi/id/product_uuid)
  export MOVER_APPLIANCE_UUID
fi
if [ "${MOVER_SKIP_BUILD:-0}" = 1 ]; then
  docker image inspect "$MOVER_IMAGE" >/dev/null || mover_fail 'The selected offline image is not loaded. Load its archive with docker load first.'
elif [ "${MOVER_BUILD:-0}" = 1 ]; then
  printf '%s\n' 'Building ESXi Mover. Docker may need more RAM than the running app.'
  mover_compose build
else
  printf '%s\n' "Pulling prebuilt image: $MOVER_IMAGE"
  if mover_pull_output=$(mover_compose pull mover 2>&1); then
    printf '%s\n' "$mover_pull_output"
  else
    printf '%s\n' "$mover_pull_output" >&2
    mover_auth_error=0
    case "$(printf '%s' "$mover_pull_output" | tr '[:upper:]' '[:lower:]')" in
      *unauthorized*|*'authentication required'*|*'error from registry: denied'*|*'denied: requested access'*) mover_auth_error=1 ;;
    esac
    case "$MOVER_IMAGE:$mover_auth_error" in
      ghcr.io/*:1)
        if [ -t 0 ]; then
          printf '%s\n' 'GHCR requires authentication. Enter your GitHub username and a classic token with read:packages at the Docker login prompts.'
          docker login ghcr.io || mover_fail 'GHCR login failed.'
          mover_compose pull mover || mover_fail 'Image pull still failed after login; check package access and token permissions.'
        else
          mover_fail 'GHCR requires authentication. Run docker login ghcr.io with a classic token with read:packages, then retry.'
        fi
        ;;
      *) mover_fail 'Image download failed. Check the registry error above; the application has not started.' ;;
    esac
  fi
fi
printf '%s\n' 'Open https://<appliance-ip>:8443 and use the token printed below.' 'Keep this console attached. Do not stop/restart the appliance during migration.' 'The HTTPS certificate and admin token are ephemeral. Container logging to disk is disabled.'
exec docker compose -f "$mover_compose_file" up --no-build --pull never --abort-on-container-exit
