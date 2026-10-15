package main

import (
	"sync"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// logins holds one apply.Login per destination for the whole run, so a site
// whose transport is built several times (a map of every site, then the one
// a stage needs) asks its login user's UID once.
//
// The map lives for one process run, keyed by destination (the transport's
// Describe), and nothing in it is ever dropped: a cached UID is trusted for
// the rest of the process. Any code that creates or removes a user and then
// reaches that user in the same process must empty the map first, as the
// tests' resetLogins does, so the first command after it asks the UID again.
var (
	loginsMu sync.Mutex
	logins   = map[string]*apply.Login{}
)

// loginFor is the Login every transport to destination shares in this run.
// connected is kept from the first call for a destination, which is the
// call that made its Login.
func loginFor(destination string, connected func()) *apply.Login {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if l, ok := logins[destination]; ok {
		return l
	}
	l := apply.NewLogin(connected)
	logins[destination] = l
	return l
}
