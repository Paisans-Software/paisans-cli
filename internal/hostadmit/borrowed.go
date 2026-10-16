package hostadmit

// The two functions below are Task 7's rootsAlone and groupOf, copied verbatim so
// this lane builds before that task merges. When it merges, the compiler
// names each one twice: delete this file.

// rootsAlone reports whether a directory's ls -ldn mode and owner say it is
// a directory, not a link, owned by root, with no group or other write bit
// and no ACL.
func rootsAlone(d DirState) bool {
	m := d.Mode
	return d.Present && len(m) == 10 && m[0] == 'd' && m[5] != 'w' && m[8] != 'w' && d.UID == 0
}

// groupOf is the user's primary group, its own name for a user not made
// yet (useradd's default on Debian and Ubuntu); a step using it then is
// provisional, and rebuilt with the group read.
func groupOf(p *planner) string {
	if p.f.User.Exists {
		return p.f.User.Group
	}
	return p.in.Request.User
}
