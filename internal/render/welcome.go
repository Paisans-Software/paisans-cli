package render

import (
	"slices"
	"strings"
	"unicode"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// communityWords are the words that already say a name is a community's, so
// CommunityRoomName does not append "Community" to a name holding one.
var communityWords = []string{
	"Community", "Communities", "Collective", "Commons", "Club", "Society",
	"Guild", "Circle", "Network", "Group", "Hub", "Crew",
}

// CommunityRoomName is the default name of the Matrix room the engagement
// bot welcomes newcomers in: the community's name, with " Community"
// appended unless a whole word of it is one of communityWords, in any case.
// A word is a run of letters and digits, so "Riverside-Club" holds Club and
// "Clubhouse" does not. A blank name gives "Community".
func CommunityRoomName(community string) string {
	name := strings.TrimSpace(community)
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, word := range words {
		for _, w := range communityWords {
			if strings.EqualFold(word, w) {
				return name
			}
		}
	}
	return strings.TrimSpace(name + " Community")
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

// AgentClientID is the client id of the Mbin agent client an engagement app
// welcomes newcomers through, which the mbin app declares.
const AgentClientID = "engagement-agent"

// CommunityRoom is the engagement app's community room name and alias
// localpart: its community_room_name setting, or CommunityRoomName of the
// name the digest calls the community by. Derived is true for the second,
// which validate then checks at community.name.
func CommunityRoom(cfg *config.Config, app string) (name, alias string, derived bool) {
	name, _ = cfg.Apps[app].Settings["community_room_name"].(string)
	if name == "" {
		community := cfg.Community.Name
		if mbin, ok := cfg.EngagementSource(); ok && community == "" {
			_, community = mbinTitles(cfg, mbin)
		}
		name, derived = CommunityRoomName(community), true
	}
	return name, RoomAliasLocalpart(name), derived
}

// welcomeDailyLimit is the app's welcome_email_daily_limit setting, 50 when
// unset.
func welcomeDailyLimit(app config.App) int {
	if n, ok := app.Settings["welcome_email_daily_limit"].(int); ok {
		return n
	}
	return 50
}

// operatorEmails is every operator's email, comma separated, by operator
// name.
func operatorEmails(cfg *config.Config) string {
	names := make([]string, 0, len(cfg.Operators))
	for name := range cfg.Operators {
		names = append(names, name)
	}
	slices.Sort(names)
	emails := make([]string, 0, len(names))
	for _, name := range names {
		emails = append(emails, cfg.Operators[name].Email)
	}
	return strings.Join(emails, ",")
}

// consumerValues is what the app called app shows the providers of the
// roles it consumes, beside its shared secrets: for an engagement app, its
// community room's alias and the Mbin agent client's id and username.
// Other apps show none.
func (p *planner) consumerValues(app string) map[string]string {
	if p.cfg.Apps[app].Blueprint != config.BlueprintEngagement {
		return map[string]string{}
	}
	_, alias, _ := CommunityRoom(p.cfg, app)
	username, _ := p.cfg.Apps[app].Settings["bot_localpart"].(string)
	if username == "" {
		username = "community-bot"
	}
	return map[string]string{
		"community_room_alias": alias,
		"mbin_agent_client_id": AgentClientID,
		"mbin_agent_username":  username,
	}
}
