#!/bin/bash

set -eu

SCRIPT_NAME="$(basename "$0")"

echo "${SCRIPT_NAME} is running... "


function check_status() {
  # first param is error message to print in case of error
  if [ $? -ne 0 ]; then
    if [ -n "$1" ]; then
      echo "$1"
    fi

    # Exit 255 to pass signal to xargs to abort process with code 1, in other cases xargs will complete with 0.
    exit 255
  fi
}

function sync_dep() {
  dep=$1

  echo "[INFO]: Going to sync ${dep}"

  go mod tidy -C "${dep}"
  go work sync -C "${dep}"

  check_status "[FAIL]: sync [${dep}] failed!"

  echo "[SUCCESS]: sync [${dep}] finished."
}

export -f sync_dep
export -f check_status

function sync_deps() {
  go list -f '{{.Dir}}' -m |
    xargs -n 1 -P 1 -I {} bash -c 'sync_dep "$@"' _ {}
}

sync_deps

echo "[INFO]: Syncing vendor"
go work vendor
check_status "[FAIL]: sync vendor failed!"
echo "[SUCCESS]: sync vendor finished."

echo "${SCRIPT_NAME} is finished."