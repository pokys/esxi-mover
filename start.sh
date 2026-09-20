#!/bin/sh
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"

command -v docker >/dev/null 2>&1 ||
  { printf '%s\n' 'Install Docker first: apk add docker docker-cli-compose' >&2; exit 1; }

# Alpine Live refuses to start Docker when its networking service fails to
# parse /etc/network/interfaces. Retry without dependencies; either way this
# script never rewrites the interfaces file and never restarts networking.
if ! docker info >/dev/null 2>&1; then
  command -v rc-service >/dev/null 2>&1 ||
    { printf '%s\n' 'Start the Docker daemon, then run this script again.' >&2; exit 1; }
  if ! rc-service docker start; then
    rc-service sysfs start
    rc-service cgroups start
    rc-service --nodeps docker start
  fi
  tries=0
  until docker info >/dev/null 2>&1; do
    if [ "$tries" -ge 30 ]; then
      printf '%s\n' 'Docker did not become ready within 30 seconds. Check rc-service docker status.' >&2
      exit 1
    fi
    sleep 1
    tries=$((tries + 1))
  done
fi

# Lets the tool recognize this appliance and refuse to migrate itself.
if [ -z "${MOVER_APPLIANCE_UUID:-}" ] && [ -r /sys/class/dmi/id/product_uuid ]; then
  MOVER_APPLIANCE_UUID=$(cat /sys/class/dmi/id/product_uuid)
  export MOVER_APPLIANCE_UUID
fi

printf '%s\n' 'Open https://<appliance-ip>:8443 and sign in with the admin token printed below.'
exec docker compose up --abort-on-container-exit
