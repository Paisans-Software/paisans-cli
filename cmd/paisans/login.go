package main

import (
	"sync"

	"github.com/paisans-software/paisans-cli/internal/apply"
)

// logins holds one apply.Login per destination for the whole run, so a site
// whose transport is built several times (a map of every site, then the one
// a stage needs) asks its login user's UID once.
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
