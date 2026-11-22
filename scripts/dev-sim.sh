#!/usr/bin/env bash
# Run the gateway against simulated devices for local testing: starts hwsim
# in the background (unless it already runs) and the gateway in the
# foreground with the simulator's devices and price sources. Stop with
# Ctrl+C or scripts/dev-sim.sh stop.
#
# Both are built to <data-dir>/bin and listen on 127.0.0.1 only, so Windows
# does not ask for firewall access on every run (go run builds a new exe in a
# new temp folder each time). The gateway is at http://localhost:8080.
#
#   scripts/dev-sim.sh [data-dir]   # default data dir: data/dev-sim
#   scripts/dev-sim.sh stop         # stop gateway and simulator
#
# HWSIM_ARGS passes options to hwsim, e.g. the room simulation:
#   HWSIM_ARGS="-speed 30 -room-start 17" scripts/dev-sim.sh
set -euo pipefail
cd "$(dirname "$0")/.."

stop() {
	if command -v taskkill >/dev/null 2>&1; then
		taskkill //F //IM hwlocal.exe >/dev/null 2>&1 || true
		taskkill //F //IM hwsim.exe >/dev/null 2>&1 || true
	else
		pkill -x hwlocal 2>/dev/null || true
		pkill -x hwsim 2>/dev/null || true
	fi
}

if [ "${1:-}" = "stop" ]; then
	stop
	exit 0
fi

data="${1:-data/dev-sim}"
mkdir -p "$data"

exe=""
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) exe=.exe ;; esac
go build -o "$data/bin/hwsim$exe" ./cmd/hwsim
go build -o "$data/bin/hwlocal$exe" ./cmd/hwlocal

# hwsim listens on :18080 (prices), 18001-18005 and 18444 (devices) and
# 18551 (MHI air conditioner), 6668 (Tuya radiator), 18025 (SMTP)
if ! (exec 3<>/dev/tcp/127.0.0.1/18080) 2>/dev/null; then
	# shellcheck disable=SC2086 # HWSIM_ARGS is split into options on purpose
	"$data/bin/hwsim$exe" -host 127.0.0.1 ${HWSIM_ARGS:-} >"$data/hwsim.log" 2>&1 &
	for _ in $(seq 1 60); do
		[ -f data/hwsim-ca.pem ] && (exec 3<>/dev/tcp/127.0.0.1/18080) 2>/dev/null && break
		sleep 0.5
	done
fi

export HWLOCAL_HTTP_ADDR=127.0.0.1:8080
export HWLOCAL_DATA_DIR="$data"
export HWLOCAL_DEVICE_CA=data/hwsim-ca.pem
export HWLOCAL_DISCOVERY=false
export HWLOCAL_DEVICES=127.0.0.1:18001,127.0.0.1:18002,127.0.0.1:18003,127.0.0.1:18004,127.0.0.1:18005,127.0.0.1:18444
export HWLOCAL_NORDPOOL_URL=http://127.0.0.1:18080
export HWLOCAL_ENERGYZERO_URL=http://127.0.0.1:18080
# the fake tado of the room simulation (docs/KAMERSIMULATIE.md)
export HWLOCAL_TADO_URL=http://127.0.0.1:18080/tado
# the fake mijn.ista.nl (add an ista plugin with sim@example.org / sim)
export HWLOCAL_ISTA_URL=http://127.0.0.1:18081
export HWLOCAL_ISTA_PACE=-1ns
export HWLOCAL_PRICE_BACKFILL_FROM="${HWLOCAL_PRICE_BACKFILL_FROM:-$(date -d yesterday +%F 2>/dev/null || date +%F)}"
exec "$data/bin/hwlocal$exe"
