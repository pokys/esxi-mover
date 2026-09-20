#!/bin/sh
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
command -v docker >/dev/null 2>&1 || { printf '%s\n' 'Install Docker first: apk add docker docker-cli-compose'; exit 1; }
if ! docker info >/dev/null 2>&1; then
  if command -v rc-service >/dev/null 2>&1; then
    if ! rc-service docker start; then
      printf '%s\n' \
        'OpenRC could not start Docker. ESXi Mover has not started.' \
        'If networking failed with "could not parse /etc/network/interfaces", see the Alpine Live troubleshooting section in README.md.' \
        'Do not restart a working network or overwrite its configuration to start this application.' >&2
      exit 1
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
    printf '%s\n' 'Start the Docker daemon, then run this script again.'
    exit 1
  fi
fi
docker compose version >/dev/null
if [ -z "${MOVER_APPLIANCE_UUID:-}" ] && [ -r /sys/class/dmi/id/product_uuid ]; then
  MOVER_APPLIANCE_UUID=$(cat /sys/class/dmi/id/product_uuid)
  export MOVER_APPLIANCE_UUID
fi
if [ "${MOVER_SKIP_BUILD:-0}" = 1 ]; then
  docker image inspect "${MOVER_IMAGE:-esxi-mover:local}" >/dev/null
elif [ -n "${MOVER_IMAGE:-}" ]; then
  printf '%s\n' "Pulling prebuilt image: $MOVER_IMAGE"
  docker compose pull mover
else
  printf '%s\n' 'Building ESXi Mover. Docker may need more RAM than the running app.'
  docker compose build
fi
printf '%s\n' 'Open https://<appliance-ip>:8443 and use the token printed below.' 'Keep this console attached. Do not stop/restart the appliance during migration.' 'The HTTPS certificate and admin token are ephemeral. Container logging to disk is disabled.'
exec docker compose up --no-build --pull never --abort-on-container-exit
