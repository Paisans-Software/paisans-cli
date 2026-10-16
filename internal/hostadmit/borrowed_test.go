package hostadmit_test

// The fixture below is Task 6's home, copied verbatim so this lane's
// plans can be drawn before that task merges. When it merges, the compiler
// names it twice: delete this file.

// home is a host on a home LAN, as one admitted with --mesh-lan is.
func home() *fakeHost { return current().withUser().with("lan", "192.168.1.20/24") }
