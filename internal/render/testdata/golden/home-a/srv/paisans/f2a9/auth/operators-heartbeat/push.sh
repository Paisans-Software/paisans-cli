#!/bin/sh
# Rendered by paisans. Do not edit: `paisans apply` overwrites this file.
#
# The operators heartbeat of auth, from its instance on home-a.
# Once a minute it asks the operator reconciler beside this instance whether
# the operators check out and passes the answer on to each URL in
# OPERATORS_HEARTBEAT_URLS, which heartbeat.env beside this file sets. Every
# instance pushes to the same token, so the monitor hears whichever one is
# active:
#
#   200 standby   this instance is a standby: push nothing, the active one
#                 speaks for the deployment.
#   200           every check holds: push success.
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
reconciler=http://reconciler:1412
agent="paisans-operators-heartbeat (auth@home-a)"

# push sends one push to every monitor in $1: $2 is the status segment, empty
# for success, and $3 the body of a failure.
push() {
	for url in $1; do
		if [ -z "$2" ]; then
			curl -fsS -o /dev/null -m 10 --retry 2 -A "$agent" "$url" \
				|| echo "operators-heartbeat: push failed at $(date -u +%Y-%m-%dT%H:%M:%SZ)" >&2 &
		else
			# Plain text, so the monitor's form and JSON parsers leave the
			# body alone and it is kept as sent; from stdin, so a reason
			# starting with @ is never read as a file name.
			printf '%s' "$3" | curl -fsS -o /dev/null -m 10 --retry 2 -A "$agent" \
				-H 'Content-Type: text/plain' --data-binary @- "$url/$2" \
				|| echo "operators-heartbeat: push failed at $(date -u +%Y-%m-%dT%H:%M:%SZ)" >&2 &
		fi
	done
}

# relay asks the reconciler's path $1 and passes its answer to the monitors
# in $2.
relay() {
	# The body, a newline, and the status curl writes last: 000 when nothing
	# answered.
	reply=$(curl -s -m 10 -A "$agent" -w '\n%{http_code}' "$reconciler$1" 2>/dev/null) || reply='
000'
	code=${reply##*
}
	body=$(printf '%s\n' "$reply" | head -n 1)
	case "$code" in
	200)
		[ "$body" = standby ] || push "$2" "" ""
		;;
	503)
		push "$2" fail "$body"
		;;
	*)
		echo "operators-heartbeat: the reconciler did not answer $1 (status $code) at $(date -u +%Y-%m-%dT%H:%M:%SZ)" >&2
		;;
	esac
}

while true; do
	sleep 60 &
	relay /healthz "$OPERATORS_HEARTBEAT_URLS"
	wait
done
