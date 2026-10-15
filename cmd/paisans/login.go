package main

import (
	"flag"
	"fmt"
	"sync"

	"github.com/paisans-software/paisans-cli/internal/apply"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/ui"
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

// loginReporter is the reporter a recorded login user is told through: the
// running command's, set by commonFlags (routeLoginNotes), as the retry lines
// are. Nil says nothing.
var loginReporter ui.Reporter

// routeLoginNotes sends the notes recordSSHUser makes to r.
func routeLoginNotes(r ui.Reporter) { loginReporter = r }

// recordLogins is whether the run writes a login user it records: true
// once a command run with --execute that changes hosts has started, set by
// commonFlags (executesOnHosts), and left so for the steps that command
// runs in this process. Any other run, read-only commands included, writes
// nothing and only says what --execute records, as an architecture read
// does.
var recordLogins bool

// executeChangesNoHost names the commands whose --execute writes only on
// this workstation, or only tidies paisans-stack.yaml, and so changes no
// host: they record nothing either.
var executeChangesNoHost = map[string]bool{"doctor": true, "config pull": true, "config recover": true}

// executesOnHosts reports whether fs, parsed, is a command run with
// --execute that changes hosts.
func executesOnHosts(fs *flag.FlagSet) bool {
	f := fs.Lookup("execute")
	if f == nil || executeChangesNoHost[fs.Name()] {
		return false
	}
	g, ok := f.Value.(flag.Getter)
	if !ok {
		return false
	}
	on, _ := g.Get().(bool)
	return on
}

// recordMu takes turns between recordSSHUser's writes: a command that
// reaches several sites at once can log in to two of them together, and
// SetSSHUser reads the file and writes it whole.
var recordMu sync.Mutex

// recordSSHUser is what a transport calls once it has logged in to site as
// user, the deployment's own, which the declaration at path leaves out of
// ssh.user. In a run with --execute it writes the name there, so the file
// says who logs in, and a later run reads it as declared. Any other run
// writes nothing and says what --execute records; the run goes on as user
// either way, which is already what the file means. The login it records
// has already worked, so a write that fails is said and is never the
// command's failure.
func recordSSHUser(path, site, user string) func() {
	return func() {
		recordMu.Lock()
		defer recordMu.Unlock()
		r := loginReporter
		if !recordLogins {
			if r != nil {
				r.Note(fmt.Sprintf("%s: ssh.user %s, recorded in %s by --execute", site, user, config.FileName), "")
			}
			return
		}
		err := config.SetSSHUser(path, site, user)
		if r == nil {
			return
		}
		if err != nil {
			r.Note(fmt.Sprintf("%s: ssh.user %s could not be recorded in %s", site, user, config.FileName),
				err.Error()+". The command goes on as "+user+"; write user: "+user+" under sites."+site+".ssh yourself, or leave it for the next run with --execute to try.")
			return
		}
		r.Note(fmt.Sprintf("%s: ssh.user %s recorded in %s", site, user, config.FileName), "")
	}
}
