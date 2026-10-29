package apply

import (
	"errors"
	"fmt"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/version"
)

// NewerToolkitError is a site whose manifest was last written by a newer
// release of paisans than this one. That release may put things on the host
// this one does not know about, and an apply by this one would write over
// them with what it does know, so the apply is refused.
type NewerToolkitError struct {
	Site string
	// Build is the paisans that last applied the site.
	Build version.Build
}

func (e *NewerToolkitError) Error() string {
	return fmt.Sprintf("%s was last applied by paisans %s, and this is paisans %s, so it may not know everything that release put on the host. Nothing was changed on %s. Upgrade paisans to %s or later, and run the command again", e.Site, e.Build, version.Version, e.Site, e.Build.Version)
}

// CheckToolkit reads the manifest on site and refuses, with a
// *NewerToolkitError, one that a newer release of paisans wrote. A build of
// the same release passes, whatever its commit, as does an older release and
// a host with no manifest yet, as version.Newer orders them. A manifest whose
// version this paisans cannot order is refused as readManifest refuses it.
func CheckToolkit(site string, t Transport, d deployment.Deployment) error {
	_, m, err := readManifest(t, d)
	if err != nil {
		return fmt.Errorf("%s: %w", site, err)
	}
	if m.Build.Version == "" {
		return nil
	}
	if newer, err := version.Newer(m.Build.Version, version.Version); err == nil && newer {
		return &NewerToolkitError{Site: site, Build: m.Build}
	}
	return nil
}

// unreadError is a manifest the transport could not read: the host did not
// answer, as against a manifest it answered with that is not one.
type unreadError struct{ err error }

func (e *unreadError) Error() string { return e.err.Error() }
func (e *unreadError) Unwrap() error { return e.err }

// Unread reports whether err is CheckToolkit failing to read the manifest at
// all, rather than reading one it refuses.
func Unread(err error) bool {
	var u *unreadError
	return errors.As(err, &u)
}
