#!/usr/bin/env bash
set -euo pipefail

# 2) Ensure Go toolchain works
go version

# 3) Run
export CAN_IFACE=vcan0
export CAN_MAP=can_map.csv
export HTTP_ADDR=127.0.0.1:8080

go mod tidy
go run .
