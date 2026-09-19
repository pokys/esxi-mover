#!/bin/sh
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
command -v docker >/dev/null 2>&1 || { printf '%s\n' 'Install Docker first: apk add docker docker-cli-compose'; exit 1; }
if ! docker info >/dev/null 2>&1; then
  if command -v rc-service >/dev/null 2>&1; then
    rc-service docker start
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
