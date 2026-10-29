package render

import "strings"

// communityWords are the words that already say a name is a community's, so
// CommunityRoomName does not append "Community" to a name holding one.
var communityWords = []string{
	"Community", "Communities", "Collective", "Commons", "Club", "Society",
	"Guild", "Circle", "Network", "Group", "Hub", "Crew",
}

// CommunityRoomName is the default name of the Matrix room the engagement
// bot welcomes newcomers in: the community's name, with " Community"
// appended unless a whole word of it is one of communityWords, in any case.
func CommunityRoomName(community string) string {
	name := strings.TrimSpace(community)
	for _, word := range strings.Fields(name) {
		for _, w := range communityWords {
			if strings.EqualFold(word, w) {
				return name
			}
		}
	}
	return name + " Community"
}

// RoomAliasLocalpart is the localpart of the community room's alias: the
// name lowercased, each run of characters outside a-z and 0-9 replaced by
// one "-", with no "-" at either end. A name with no such character has
// none, and validate refuses it.
func RoomAliasLocalpart(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}
