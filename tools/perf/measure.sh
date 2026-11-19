#!/usr/bin/env bash
# Measure the CPU and memory of one program run in a tmux pane.
#
#   tools/perf/measure.sh [--skip <s>] <binary> <seconds> [args...]
#
# The program runs as the only process of a pane in its own tmux server, on
# the private socket "av-perf-<pid of this script>", at 100 x 30. The script
# samples that one PID every second for <seconds> seconds and prints three
# lines, numbers only:
#
#   cpu_avg_pct=<n.n>   CPU time used between the first and the last sample,
#                       over the wall time between them, in percent of one core
#   cpu_max_pct=<n.n>   the same over the busiest window between two samples
#   rss_peak_mb=<n.n>   the largest resident set size seen, in MB
#
# --skip <s> leaves the first <s> seconds (counted from the launch) out of the
# two CPU lines, so the start-up scan does not count as load; rss_peak_mb
# always covers the whole run. <seconds> is the length of the whole run.
#
# CPU comes from the cumulative CPU time of the PID ("ps -o time="), not from
# "ps -o %cpu", whose meaning differs between systems. The environment goes to
# the program unchanged, so the caller sets HOME and the like. The script
# never prints the path, the arguments or the output of the program.
#
# The only way the script stops the program is "kill-server" on its own
# socket, then the socket file is removed (never the other way round: a
# removed socket file orphans the server). It never stops a process by name.
#
# tmux reads a program argument that ends in ";" as a command separator, so
# do not pass such an argument.
#
# Needs bash, tmux, perl (Time::HiRes, for sub-second wall time) and awk.
# Linux "ps -o time=" has a resolution of one second; macOS has 10 ms, so the
# one-second windows are exact only on macOS.
set -euo pipefail

usage() {
	echo "usage: measure.sh [--skip <seconds>] <binary> <seconds> [args...]" >&2
	exit 2
}

skip=0
if [ "${1:-}" = "--skip" ]; then
	[ $# -ge 2 ] || usage
	skip=$2
	shift 2
	case $skip in '' | *[!0-9]*) usage ;; esac
fi
[ $# -ge 2 ] || usage
bin=$1
secs=$2
shift 2
case $secs in '' | *[!0-9]*) usage ;; esac
[ "$secs" -ge $((skip + 2)) ] || usage
if [ ! -f "$bin" ] || [ ! -x "$bin" ]; then
	echo "measure.sh: the binary is not an executable file" >&2
	exit 2
fi
for tool in tmux perl awk ps; do
	command -v "$tool" >/dev/null || {
		echo "measure.sh: $tool is not installed" >&2
		exit 2
	}
done
# env runs the program with execvp, so the pane PID is the program's PID, with
# no shell in between (tmux runs a single command word through a shell).
case $bin in /*) ;; *) bin=$PWD/$bin ;; esac

sock="av-perf-$$"
work=$(mktemp -d)
samples=$work/samples
pid=""
sock_path=""

now() { perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; }

cleanup() {
	trap - EXIT INT TERM
	# kill-server first; the socket file goes only after the server is gone.
	tmux -L "$sock" kill-server >/dev/null 2>&1 || true
	if [ -n "$sock_path" ]; then rm -f "$sock_path"; fi
	if [ -n "$pid" ]; then
		# kill-server sends SIGHUP to the pane. A program that ignores it
		# gets SIGKILL after 5 s: this PID is the one this script started.
		for _ in 1 2 3 4 5 6 7 8 9 10; do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.5
		done
		if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
	fi
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

fail() {
	echo "measure.sh: $1" >&2
	exit 1
}

# remain-on-exit is set before the pane starts, so a program that exits at
# once leaves its pane (and the server, and the socket path to remove) behind.
t_launch=$(now)
tmux -L "$sock" -f /dev/null start-server \; set-option -g remain-on-exit on \; \
	new-session -d -s perf -x 100 -y 30 -c "$work" -- env "$bin" "$@" >/dev/null 2>&1 ||
	fail "tmux could not start the pane"
sock_path=$(tmux -L "$sock" display-message -p -t perf '#{socket_path}' 2>/dev/null) || sock_path=""
pid=$(tmux -L "$sock" list-panes -t perf -F '#{pane_pid}' 2>/dev/null | head -n 1) || pid=""
case $pid in '' | *[!0-9]*) fail "no pane PID" ;; esac

# Wait up to 2 s for env to exec the program; sample only that process.
# Linux cuts the name in "ps -o comm=" to 15 characters, so only a prefix of
# the program name must match.
want=${bin##*/}
is_program() {
	local name
	name=$(ps -o comm= -p "$pid" 2>/dev/null || true)
	name=${name##*/}
	[ -n "$name" ] && case $want in "$name"*) return 0 ;; esac
	return 1
}
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
	if is_program; then break; fi
	sleep 0.1
done
is_program || fail "the pane process is not the program, or the program exited at once"

: >"$samples"
while :; do
	out=$(ps -o time=,rss= -p "$pid" 2>/dev/null) || out=""
	[ -n "$out" ] || fail "the program exited after $(awk -v a="$(now)" -v b="$t_launch" 'BEGIN { printf "%.0f", a - b }') s"
	echo "$(now) $out" >>"$samples"
	elapsed=$(awk -v a="$(now)" -v b="$t_launch" 'BEGIN { printf "%.3f", a - b }')
	if awk -v e="$elapsed" -v s="$secs" 'BEGIN { exit !(e >= s) }'; then break; fi
	sleep 1
done

# Each sample line is: wall-seconds cpu-time rss-kb. A sample counts for the
# CPU lines when it is at least skip seconds after the launch.
LC_ALL=C awk -v t0="$t_launch" -v skip="$skip" '
function cpu(t,   d, p, a, n, i, s) {
	d = 0
	if (index(t, "-")) { split(t, p, "-"); d = p[1]; t = p[2] }
	n = split(t, a, ":")
	s = 0
	for (i = 1; i <= n; i++) s = s * 60 + a[i]
	return d * 86400 + s
}
{
	w = $1; c = cpu($2); r = $3
	if (r > rss) rss = r
	if (w - t0 < skip) next
	if (!n) { w0 = w; c0 = c }
	else if (w > pw) {
		p = (c - pc) / (w - pw) * 100
		if (p > max) max = p
	}
	n++; pw = w; pc = c
}
END {
	if (n < 2 || pw <= w0) exit 1
	printf "cpu_avg_pct=%.1f\ncpu_max_pct=%.1f\nrss_peak_mb=%.1f\n", (pc - c0) / (pw - w0) * 100, max, rss / 1024
}' "$samples" || fail "too few samples"
