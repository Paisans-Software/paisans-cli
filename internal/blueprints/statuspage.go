package blueprints

import "github.com/paisans-software/paisans-cli/internal/config"

// PublicStatusPageSetting is the uptime blueprint's switch for its public
// status page, which shows the checks of each app at its public hostname and
// nothing else. README.md, "Monitoring", has what it shows.
const PublicStatusPageSetting = "public_status_page"

// PublicStatusPage reports whether an uptime app serves its public status
// page: true unless settings.public_status_page is false. A value that is not
// a boolean reads as on here, and validate refuses it by type.
func PublicStatusPage(app config.App) bool {
	if on, ok := app.Settings[PublicStatusPageSetting].(bool); ok {
		return on
	}
	return true
}
