package selfcheck

// UsePorts points fetch at test servers' ports for https and http, and
// returns what puts the real ones back.
func UsePorts(https, http string) func() {
	saved := ports
	ports = map[string]string{"https": https, "http": http}
	return func() { ports = saved }
}
