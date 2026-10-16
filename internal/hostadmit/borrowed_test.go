package hostadmit_test

// The fixture below is Task 5's current, copied verbatim so this lane's
// plans can be drawn before that task merges. When it merges, the compiler
// names it twice: delete this file.

// current is a host with Docker 29.9.1 and its extras installed and the
// profile loaded, so the user's tests see only the user's steps change.
func current() *fakeHost {
	return newBlank().with("engine", "29.9.1").with("setuptool", "present").with("rootlesskit-profile", "loaded").
		with("pkg", "docker-ce installed").add("pkg", "docker-ce-rootless-extras installed").add("pkg", "uidmap installed").add("pkg", "nftables installed").
		with("system-socket", "present").
		answer("docker compose version", "compose present\npkg docker-ce install ok installed\npkg docker-ce-rootless-extras install ok installed\npkg uidmap install ok installed\npkg nftables install ok installed\npkg wireguard-tools install ok installed\npkg ufw install ok installed\npkg git install ok installed\nkeyring present\narch amd64\n")
}
