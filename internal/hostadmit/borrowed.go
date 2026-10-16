package hostadmit

import (
	"crypto/sha256"
	"encoding/hex"
)

// The three functions below are Task 5's sha and Task 7's rootsAlone and
// groupOf, copied verbatim so this lane builds before those tasks merge.
// When they merge, the compiler names each one twice: delete this file.

// sha is content's hex SHA-256, as the probe prints a file's checksum.
func sha(content string) string {
	s := sha256.Sum256([]byte(content))
	return hex.EncodeToString(s[:])
}

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
