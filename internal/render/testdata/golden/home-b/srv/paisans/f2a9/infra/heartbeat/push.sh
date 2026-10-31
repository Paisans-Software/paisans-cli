#!/bin/sh
# Rendered by paisans. Do not edit: `paisans deployment apply` overwrites this file.
#
# The heartbeat of site home-b: one request a minute to each URL in
# HEARTBEAT_URLS, which heartbeat.env sets. A cycle is one minute from its
# start whatever the pushes take, since the sleep runs beside them: a monitor
# that is slow or gone delays no other monitor's push and never stretches the
# cadence the monitors measure staleness against. Each push gives up after
# 10 s and retries twice, so a failed one takes about 33 s at most, well
# inside the minute. The body is dropped and a push's errors are held until
# its retries are spent: one that recovers on a retry logs nothing, and one
# that fails every attempt logs that it failed and then curl's error for each
# attempt, which names the host but never the URL, so the token stays out of
# the log. Each failure is one write, so pushes failing together do not
# interleave their lines.
set -u

failed() {
	printf 'heartbeat: push failed at %s\n%s' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${1:+$1
}" >&2
}

while true; do
	sleep 60 &
	for url in $HEARTBEAT_URLS; do
		err=$(curl -fsS -o /dev/null -m 10 --retry 2 -A "paisans-heartbeat (home-b)" "$url" 2>&1) \
			|| failed "$err" &
	done
	wait
done
