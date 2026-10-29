package render

import (
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// engagementValues is what the engagement blueprint's templates need beyond
// any app's: where the deployment's one Mbin is, and what it calls itself.
type engagementValues struct {
	// SourceDSN is the connection to the Mbin app's database as the digest's
	// read only role (ReaderRole), through the digest's own site's HAProxy
	// like its own database.
	SourceDSN string
	// SourceURL is the Mbin app's public URL, which every link in the digest
	// points into.
	SourceURL string
	// InstanceTitle is the name Mbin renders as KBIN_TITLE (mbinTitles).
	// MetaTitle names the community in the digest: its name, or without one
	// the name Mbin renders as KBIN_META_TITLE.
	InstanceTitle string
	MetaTitle     string
	// HomeURL is where the digest's hostname sends a visitor with no digest to
	// read: the account app's public URL, the first by name, or the
	// community's domain in a deployment without one.
	HomeURL string
	// CommunityRoomName is the Matrix room the bot announces newcomers in,
	// and CommunityRoomAlias its alias's localpart (communityRoom).
	CommunityRoomName  string
	CommunityRoomAlias string
	// WelcomeDailyLimit is how many members the daily welcome email reaches
	// at most.
	WelcomeDailyLimit int
	// OperatorEmails is the operators' addresses, comma separated, in the
	// order of their names: who hears about it when the bot cannot post.
	OperatorEmails string
	// AgentClientID is the Mbin agent client's id, AgentClientID.
	AgentClientID string
}

// ReaderRole is the Postgres role the engagement app reads Mbin's database
// as. apply creates it with read only grants, and must name it exactly as the
// render did.
func ReaderRole(engagementApp string) string { return DBIdentifier(engagementApp) + "_reader" }

// engagementValuesFor reads the deployment's one Mbin app, which validate
// requires of a deployment with an engagement app. Without one the values
// stay empty: validate has already refused the configuration.
func (p *planner) engagementValuesFor(planned plannedApp, host string, port int) engagementValues {
	home := "https://" + p.cfg.Community.Domain
	for _, name := range p.cfg.AppNames() {
		if p.cfg.Apps[name].Blueprint == config.BlueprintAccount {
			home = "https://" + p.cfg.Apps[name].Hostname
			break
		}
	}
	welcome := engagementValues{
		HomeURL:           home,
		WelcomeDailyLimit: welcomeDailyLimit(p.cfg.Apps[planned.Name]),
		OperatorEmails:    operatorEmails(p.cfg),
		AgentClientID:     AgentClientID,
	}
	welcome.CommunityRoomName, welcome.CommunityRoomAlias, _ = CommunityRoom(p.cfg, planned.Name)
	mbin, ok := p.cfg.EngagementSource()
	if !ok {
		return welcome
	}
	reader := planned
	reader.DBUser, reader.DBName = ReaderRole(planned.Name), DBIdentifier(mbin)
	instance, meta := mbinTitles(p.cfg, mbin)
	if p.cfg.Community.Name != "" {
		meta = p.cfg.Community.Name
	}
	// A missing source_database_password renders an empty one here, and
	// never reaches a host: apply refuses it before any stack starts.
	welcome.SourceDSN = dsn(reader, p.appSecret(planned.Name, "source_database_password"), host, port)
	welcome.SourceURL = "https://" + p.cfg.Apps[mbin].Hostname
	welcome.InstanceTitle, welcome.MetaTitle = instance, meta
	return welcome
}

// mbinTitles is the instance and meta titles the Mbin app renders, from
// catalog:blueprints/mbin/.env.secret.tmpl's KBIN_TITLE and KBIN_META_TITLE,
// so the digest names the community exactly as Mbin's own pages do.
func mbinTitles(cfg *config.Config, app string) (instance, meta string) {
	set := blueprints.SettingsSeen(cfg.Apps[app])
	display := cfg.ClientName(app)
	instance = display
	if s, ok := set["title"].(string); ok && s != "" {
		instance = s
	}
	meta = display
	if cfg.Community.Name != "" {
		meta = fmt.Sprintf("%s · %s", display, cfg.Community.Name)
	}
	if s, ok := set["meta_title"].(string); ok && s != "" {
		meta = s
	}
	return instance, meta
}
