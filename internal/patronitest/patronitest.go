// Package patronitest holds what patronictl prints beside its answer, for
// fakes of a transport that returns stdout and stderr together.
package patronitest

// Before and After are log lines patronictl writes to stderr when it cannot
// reach one of etcd's endpoints, as a gone site's is. Either can come before
// or after its answer in the combined output.
const (
	Before = "2026-10-17 07:18:37,912 - WARNING - Retrying (Retry(total=0, connect=None, read=None, redirect=0, status=None)) after connection broken by 'ConnectTimeoutError(<urllib3.connection.HTTPConnection object at 0x7f3c2a1b2c10>, 'Connection to 10.77.32.1 timed out. (connect timeout=1.25)')': /v3beta/cluster/members\n"
	After  = "2026-10-17 07:18:38,024 - ERROR - Failed to get list of machines from http://10.77.32.1:2379/v3beta: MaxRetryError(\"HTTPConnectionPool(host='10.77.32.1', port=2379): Max retries exceeded with url: /v3beta/cluster/members (Caused by ConnectTimeoutError(<urllib3.connection.HTTPConnection object at 0x7f3c2a1b2c10>, 'Connection to 10.77.32.1 timed out. (connect timeout=1.25)'))\")\n"
)

// Wrap is out with a log line before it and one after, as patronictl's
// combined output reads when it tried an unreachable endpoint.
func Wrap(out string) string {
	if out != "" && out[len(out)-1] != '\n' {
		out += "\n"
	}
	return Before + out + After
}
