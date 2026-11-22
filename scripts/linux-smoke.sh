#!/usr/bin/env bash
# Smoke test of the Linux builds against the device simulator (hwsim).
#
#   scripts/linux-smoke.sh [amd64] [arm64] [arm7]     default: all three
#
# Cross-compiles hwlocal for each architecture plus hwsim, then runs every
# build for real: devices come online, the v2 battery pairs, prices are
# fetched, history is recorded and survives a restart, /api/stream sends
# events, and SIGTERM stops it cleanly with nothing logged at ERROR.
#
# On Linux it runs directly. Elsewhere (Git Bash on Windows) it builds with
# the host Go and runs the test inside WSL (distribution $WSL_DISTRO, default
# Ubuntu). Non-native architectures run through QEMU user emulation, which
# needs binfmt registration (Ubuntu: apt install qemu-user qemu-user-binfmt).
# The Linux side needs curl and python3.
#
# Emulation proves the binary starts and works; it says nothing about speed
# on slow hardware, mDNS on the real LAN or running as a service.
set -euo pipefail

if [ "${1:-}" = --run ]; then
	mode=run
	bindir=$2
	shift 2
else
	mode=build
fi
archs=("$@")
[ ${#archs[@]} -gt 0 ] || archs=(amd64 arm64 arm7)

if [ "$mode" = build ]; then
	root=$(cd "$(dirname "$0")/.." && pwd)
	cd "$root"
	out=$root/dist/linux-smoke
	mkdir -p "$out"
	version=$(scripts/version.sh 2>/dev/null || echo smoke)
	if [ "$(uname -s)" = Linux ]; then
		simarch=${HWSIM_GOARCH:-$(go env GOARCH)}
	else
		simarch=${HWSIM_GOARCH:-amd64}
	fi
	for a in "${archs[@]}"; do
		case $a in
		amd64 | arm64) goarch=$a goarm= ;;
		arm7) goarch=arm goarm=7 ;;
		*) echo "unknown architecture: $a (amd64, arm64, arm7)" >&2; exit 2 ;;
		esac
		echo "building hwlocal linux/$a ($version)"
		CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOARM=$goarm \
			go build -ldflags "-X main.version=$version" -o "$out/hwlocal-$a" ./cmd/hwlocal
	done
	echo "building hwsim linux/$simarch"
	CGO_ENABLED=0 GOOS=linux GOARCH=$simarch go build -o "$out/hwsim" ./cmd/hwsim

	if [ "$(uname -s)" = Linux ]; then
		exec bash "$0" --run "$out" "${archs[@]}"
	fi
	distro=${WSL_DISTRO:-Ubuntu}
	winout=$(cygpath -m "$out")
	wslout=$(MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -u root -- wslpath -u "$winout" | tr -d '\r')
	echo "running in WSL ($distro)"
	# Piped through stdin so a CRLF checkout cannot break the script.
	tr -d '\r' <"$0" | MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -u root -- \
		bash -s -- --run "$wslout" "${archs[@]}"
	exit
fi

# --- run mode (Linux) ---

for tool in curl python3; do
	command -v $tool >/dev/null || { echo "missing $tool" >&2; exit 2; }
done

work=$(mktemp -d)
simpid=
cleanup() {
	[ -n "$simpid" ] && kill "$simpid" 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT

# Copy to the Linux file system: SQLite locking on a /mnt/c mount is unreliable.
cp "$bindir"/hwlocal-* "$bindir"/hwsim "$work"/
chmod +x "$work"/*
cd "$work"

devices=127.0.0.1:18001,127.0.0.1:18002,127.0.0.1:18003,127.0.0.1:18004,127.0.0.1:18005,127.0.0.1:18444
ndev=6

if curl -s -o /dev/null --max-time 1 http://127.0.0.1:18001/api; then
	echo "port 18001 is in use: is another hwsim running?" >&2
	exit 2
fi
./hwsim -ca-out "$work/ca.pem" >hwsim.log 2>&1 &
simpid=$!
for _ in $(seq 50); do
	[ -s ca.pem ] && curl -s -o /dev/null --max-time 1 http://127.0.0.1:18001/api && break
	sleep 0.2
done

native=$(uname -m)
binfmt() {
	case $1 in
	amd64) [ "$native" = x86_64 ] || [ -e /proc/sys/fs/binfmt_misc/qemu-x86_64 ] ;;
	arm64) [ "$native" = aarch64 ] || [ -e /proc/sys/fs/binfmt_misc/qemu-aarch64 ] ;;
	arm7) [[ $native == arm* || $native == aarch64 ]] || [ -e /proc/sys/fs/binfmt_misc/qemu-arm ] ;;
	esac
}

# json EXPR: evaluates a Python expression with d = the JSON on stdin and
# devs = d's device list when it has one.
json() {
	python3 -c '
import json, sys
d = json.load(sys.stdin)
devs = d.get("devices", []) if isinstance(d, dict) else d
print(eval(sys.argv[1]))' "$1"
}

# until_ok SECONDS CMD...: retries CMD every second until it succeeds.
until_ok() {
	local n=$1
	shift
	for _ in $(seq "$n"); do
		"$@" >/dev/null 2>&1 && return 0
		sleep 1
	done
	return 1
}

failed=()
port=18100
for arch in "${archs[@]}"; do
	port=$((port + 1))
	B=http://127.0.0.1:$port
	log=$work/$arch.log
	data=$work/data-$arch
	mkdir -p "$data"
	echo
	echo "=== linux/$arch"
	if ! binfmt "$arch"; then
		echo "   FAIL cannot run $arch here: no QEMU binfmt registration (apt install qemu-user qemu-user-binfmt)"
		failed+=("$arch")
		continue
	fi
	ok=1
	pass() { echo "   ok   $*"; }
	fail() { echo "   FAIL $*"; ok=0; }

	start() {
		HWLOCAL_DATA_DIR=$data HWLOCAL_HTTP_ADDR=127.0.0.1:$port \
			HWLOCAL_DEVICE_CA=$work/ca.pem HWLOCAL_DISCOVERY=false HWLOCAL_INTERVAL=1s \
			HWLOCAL_NORDPOOL_URL=http://127.0.0.1:18080 HWLOCAL_ENERGYZERO_URL=http://127.0.0.1:18080 \
			HWLOCAL_PRICE_BACKFILL_FROM="$(date -d yesterday +%F)" \
			"$@" "$work/hwlocal-$arch" >>"$log" 2>&1 &
		pid=$!
		until_ok 30 curl -sf "$B/api/info"
	}
	stop() {
		kill -TERM "$pid" 2>/dev/null || return 1
		for _ in $(seq 20); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.5
		done
		if kill -0 "$pid" 2>/dev/null; then
			kill -KILL "$pid"
			return 1
		fi
		wait "$pid"
	}
	devs() { curl -sf "$B/api/devices"; }
	online() { [ "$(devs | json "sum(v['status'] == 'online' for v in devs)")" = "$1" ]; }
	paired() { [ "$(devs | json "any(v['api_version'] == 2 and v['paired'] for v in devs)")" = True ]; }
	points() { [ "$(curl -sf "$B/api/history/rate?device=$p1" | json "len(d.get('points') or [])")" -gt 0 ]; }

	if start env HWLOCAL_DEVICES=$devices; then
		pass "started: $(curl -s "$B/api/info" | json "d['version']" || true)"
	else
		fail "no answer on /api/info within 30 s"
	fi
	if [ $ok = 1 ]; then
		[ "$(curl -s -o /dev/null -w '%{http_code}' "$B/")" = 200 ] &&
			[ "$(curl -s -o /dev/null -w '%{http_code}' "$B/app.js")" = 200 ] &&
			pass "PWA served" || fail "PWA not served"
		if until_ok 20 online $((ndev - 1)); then pass "v1 devices online"; else fail "v1 devices not online: $(devs)"; fi
		v2=$(devs | json "next((v['id'] for v in devs if v['api_version'] == 2), '')" || true)
		code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$B/api/devices/$v2/pair")
		if [ "$code" = 202 ] && until_ok 30 paired && until_ok 10 online $ndev; then
			pass "battery paired over API v2"
		else
			fail "pairing (HTTP $code)"
		fi
		n=0
		for _ in $(seq 20); do
			n=$(curl -sf "$B/api/prices?date=$(date +%F)" | json "len(d.get('prices', d) if isinstance(d, dict) else d)" || true)
			[ "${n:-0}" -gt 0 ] 2>/dev/null && break
			n=0
			sleep 1
		done
		[ "$n" -gt 0 ] && pass "$n prices for today" || fail "no prices for today"
		p1=$(devs | json "next(v['id'] for v in devs if v['kind'] == 'p1')" || true)
		# Minute samples are written when a minute ends.
		if until_ok 90 points; then pass "history recorded"; else fail "no history after 90 s"; fi
		ev=$( (timeout 3 curl -sN "$B/api/stream" || true) | grep -c '^event:' || true)
		[ "$ev" -gt 0 ] && pass "$ev stream events in 3 s" || fail "no stream events"
		if stop; then pass "clean stop"; else fail "did not stop cleanly on SIGTERM"; fi

		# Restart on the same database: devices and the pairing token come
		# from the store, history must still be there.
		if start; then
			if until_ok 30 online $ndev && paired && points; then
				pass "restart: $ndev devices online, pairing and history kept"
			else
				fail "restart: state not kept: $(devs)"
			fi
			stop || fail "did not stop cleanly after restart"
		else
			fail "restart: no answer on /api/info"
		fi
	fi
	[ -n "${pid:-}" ] && kill -KILL "$pid" 2>/dev/null || true
	if grep -qiE 'level=ERROR|panic|fatal' "$log"; then
		fail "errors in log:"
		grep -iE 'level=ERROR|panic|fatal' "$log" | head -5 | sed 's/^/        /'
	fi
	if [ $ok = 0 ]; then
		failed+=("$arch")
		echo "   last log lines:"
		tail -n 15 "$log" | sed 's/^/        /'
	fi
done

echo
if [ ${#failed[@]} -gt 0 ]; then
	echo "FAILED: ${failed[*]}"
	exit 1
fi
echo "all passed: ${archs[*]}"
