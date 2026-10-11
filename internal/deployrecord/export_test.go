package deployrecord

// SetTags makes newTag return tags in order, for tests that need to know
// which tag compaction keeps. It returns the restore.
func SetTags(tags ...string) func() {
	saved := newTag
	newTag = func() string {
		t := tags[0]
		tags = tags[1:]
		return t
	}
	return func() { newTag = saved }
}

// UserReadCommand is the command ReadAsUser runs as the ssh user.
var UserReadCommand = userReadCommand

// DeniedMarker is what UserReadCommand prints for a record it may not read.
const DeniedMarker = deniedMarker
