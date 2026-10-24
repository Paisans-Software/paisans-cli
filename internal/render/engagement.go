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
	// InstanceTitle and MetaTitle are the names Mbin renders as KBIN_TITLE
	// and KBIN_META_TITLE (mbinTitles).
	InstanceTitle string
	MetaTitle     string
}

// ReaderRole is the Postgres role the engagement app reads Mbin's database
// as. apply creates it with read only grants, and must name it exactly as the
// render did.
func ReaderRole(engagementApp string) string { return DBIdentifier(engagementApp) + "_reader" }

// engagementValuesFor reads the deployment's one Mbin app, which validate
// requires of a deployment with an engagement app. Without one the values
// stay empty: validate has already refused the configuration.
func (p *planner) engagementValuesFor(planned plannedApp, host string, port int) engagementValues {
	mbin, ok := p.cfg.EngagementSource()
	if !ok {
		return engagementValues{}
	}
	reader := planned
	reader.DBUser, reader.DBName = ReaderRole(planned.Name), DBIdentifier(mbin)
	instance, meta := mbinTitles(p.cfg, mbin)
	return engagementValues{
		SourceDSN:     dsn(reader, p.appSecret(planned.Name, "source_database_password"), host, port),
		SourceURL:     "https://" + p.cfg.Apps[mbin].Hostname,
		InstanceTitle: instance,
		MetaTitle:     meta,
	}
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
