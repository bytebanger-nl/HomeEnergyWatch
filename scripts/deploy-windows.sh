#!/usr/bin/env bash
# Puts a new build on a Windows machine where hwlocal runs as a service
# (installed with the installer or "hwlocal service install"), over SSH:
# builds the checked-out code (version per scripts/version.sh), copies it,
# stops the service, swaps the exe (the old one stays as hwlocal.exe.old),
# starts it and shows the log of the new start (scripts/deploy-windows.ps1
# does the part on the machine, and rolls back when the new exe fails).
# The service's own flags and data are left alone.
#
#   scripts/deploy-windows.sh user@host [exe]   # exe: deploy this file instead of building
#
# The account must be an administrator on that machine, with OpenSSH Server
# and key login set up (docs/HANDLEIDING.md, "Bijwerken op afstand").
# HWLOCAL_DEPLOY_KEY is the private key (default ~/.ssh/hwlocal_deploy).
set -euo pipefail
cd "$(dirname "$0")/.."

target=${1:?usage: scripts/deploy-windows.sh user@host [exe]}
exe=${2:-}
key=${HWLOCAL_DEPLOY_KEY:-$HOME/.ssh/hwlocal_deploy}
# WarnWeakCrypto: the OpenSSH of Windows 10 has no post-quantum key exchange,
# which is fine on a LAN; without it every call prints a warning
opts=(-o BatchMode=yes -o ConnectTimeout=10 -o WarnWeakCrypto=no)
if [ -f "$key" ]; then
	opts+=(-i "$key")
fi

if [ -z "$exe" ]; then
	v=$(scripts/version.sh)
	echo "building $v"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$v" \
		-o dist/hwlocal-deploy.exe ./cmd/hwlocal
	exe=dist/hwlocal-deploy.exe
fi

# a fresh folder in the account's home (the default OpenSSH shell is cmd)
dir=hwlocal-deploy
ssh "${opts[@]}" "$target" "(if exist $dir rmdir /s /q $dir) & mkdir $dir"
echo "copying to $target"
scp -q "${opts[@]}" "$exe" "$target:$dir/hwlocal.exe"
scp -q "${opts[@]}" scripts/deploy-windows.ps1 "$target:$dir/deploy.ps1"
ssh "${opts[@]}" "$target" "powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $dir\\deploy.ps1"
