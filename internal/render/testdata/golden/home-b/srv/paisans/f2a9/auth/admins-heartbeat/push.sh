#!/bin/sh
# Rendered by paisans. Do not edit: `paisans apply` overwrites this file.
#
# The admins heartbeat of auth, from its instance on home-b.
# Once a minute it asks the admin reconciler beside this instance for the
# admins group's health and passes it on to each URL in ADMINS_HEARTBEAT_URLS,
# which heartbeat.env beside this file sets. Every instance pushes to the same
# token, so the monitor hears whichever one is active:
#
#   200 standby   this instance is a standby: push nothing, the active one
#                 speaks for the group.
#   200           the group is healthy: push success.
#   503           it is not: push to /fail, with the reconciler's one line as
#                 the body, which the monitor keeps with the ping.
#   anything else no answer from the reconciler: push nothing. Silence past
#                 the monitor's grace reads as down, which is what an active
#                 instance with no working reconciler is.
#
# The question goes to the reconciler by its service name on this stack's own
# network, with no --retry: curl's --retry counts an HTTP 503 as transient
# (curl(1), --retry, read from curl 8.7.1's manual on 2026-10-11), and here a
# 503 is the answer. Each push is the site heartbeat's (infra/heartbeat/
# push.sh): it gives up after 10 s and retries twice, and the pushes run beside
# each other and beside the minute's sleep, so a slow monitor delays no other
# and the cadence holds. Only errors are logged, naming the host but never the
# URL, so the token stays out of the log.
set -u
healthz=http://reconciler:1412/healthz
agent="paisans-admins-heartbeat (auth@home-b)"

# push sends one push to every monitor: $1 is the status segment, empty for
# success, and $2 the body of a failure.
push() {
	for url in $ADMINS_HEARTBEAT_URLS; do
		if [ -z "$1" ]; then
			curl -fsS -o /dev/null -m 10 --retry 2 -A "$agent" "$url" \
				|| echo "admins-heartbeat: push failed at $(date -u +%Y-%m-%dT%H:%M:%SZ)" >&2 &
		else
			# Plain text, so the monitor's form and JSON parsers leave the
			# body alone and it is kept as sent; from stdin, so a reason
			# starting with @ is never read as a file name.
			printf '%s' "$2" | curl -fsS -o /dev/null -m 10 --retry 2 -A "$agent" \
				-H 'Content-Type: text/plain' --data-binary @- "$url/$1" \
				|| echo "admins-heartbeat: push failed at $(date -u +%Y-%m-%dT%H:%M:%SZ)" >&2 &
		fi
	done
}

while true; do
	sleep 60 &
	# The body, a newline, and the status curl writes last: 000 when nothing
	# answered.
	reply=$(curl -s -m 10 -A "$agent" -w '\n%{http_code}' "$healthz" 2>/dev/null) || reply='
000'
	code=${reply##*
}
	body=$(printf '%s\n' "$reply" | head -n 1)
	case "$code" in
	200)
		[ "$body" = standby ] || push "" ""
		;;
	503)
		push fail "$body"
		;;
	*)
		echo "admins-heartbeat: the reconciler did not answer (status $code) at $(date -u +%Y-%m-%dT%H:%M:%SZ)" >&2
		;;
	esac
	wait
done
