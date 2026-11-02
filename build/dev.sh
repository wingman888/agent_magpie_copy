#!/usr/bin/env bash
# `make dev`: the windows and the tray (the shell) stay up while a Go change
# rebuilds and restarts only the backend behind them — the API and the
# gateway. The page refreshes what it shows in place; UI files under
# internal/gui/assets reload the page as before. A build that fails leaves
# the running backend alone. A change to the shell's own code
# (internal/gui/app.go, dev_on.go) restarts the windows too. Quitting magpie
# from the tray, or Ctrl-C, ends it all.
#
# make passes MAGPIE_ADDR, MAGPIE_DEV_UI, MAGPIE_DEV_BACKEND and
# MAGPIE_DEV_CONTROL.
set -u
cd "$(dirname "$0")/.."

shell='' backend='' watch='' fsw=''

stop() { [ -n "$1" ] && kill "$1" 2>/dev/null && wait "$1" 2>/dev/null; }

start_backend() { MAGPIE_DEV_ROLE=backend ./magpie-dev-backend app & backend=$!; }

# the shell, and a watcher that ends everything once it is quit
start_shell() {
	MAGPIE_DEV_ROLE=shell ./magpie-dev app & shell=$!
	{ while kill -0 "$shell" 2>/dev/null; do sleep 1; done; kill -TERM $$; } & watch=$!
}

stop_shell() {
	kill "$watch" 2>/dev/null; wait "$watch" 2>/dev/null
	stop "$shell"
}

cleanup() {
	trap - INT TERM
	stop_shell; stop "$backend"; kill "$fsw" 2>/dev/null
	exit 0
}
trap cleanup INT TERM

# what the build reads: Go, and the one Markdown file embedded
relevant() {
	case $1 in
	*/site/* | *_test.go) return 1 ;;
	*.go | */internal/agent/codex_prompt.md) return 0 ;;
	esac
	return 1
}

# builds to a new file and moves it in, never over a running binary
build() { go build -tags dev -o magpie-dev.new . && cp magpie-dev.new magpie-dev-backend.new && mv magpie-dev-backend.new magpie-dev-backend; }

build || exit 1
mv magpie-dev.new magpie-dev
start_backend
start_shell
exec 3< <(fswatch -r -e '/\.git/' -e '/site/' -e '/magpie-dev' "$PWD")
fsw=$!
echo "  magpie-dev · UI from internal/gui/assets, reload on save · Go changes restart the backend only · gateway $MAGPIE_ADDR"

while read -r -u 3 path; do
	relevant "$path" || continue
	changed=" $path"
	# let a burst of saves settle into one build
	while read -r -u 3 -t 0.3 more; do relevant "$more" && changed="$changed $more"; done
	echo "  go changed · rebuilding"
	if ! build; then
		echo "  build failed · the backend before it keeps running"
		continue
	fi
	stop "$backend"
	start_backend
	case $changed in
	*/internal/gui/app.go* | */internal/gui/dev_on.go*)
		echo "  window code changed · reopening the windows"
		stop_shell
		mv magpie-dev.new magpie-dev
		start_shell
		;;
	*) rm -f magpie-dev.new ;;
	esac
done
