package blueprints

import (
	"fmt"
	"regexp"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// HostnameNone is the one value of a blueprint's hostname key: the app serves
// no website, so it takes no hostname (v5). Such an app is given no public
// name, no DNS record, no certificate and no route at the gateway, and the
// monitor checks it over the mesh, where every app publishes its port. An
// operator who wrote a hostname for it would be naming something nothing
// routes or certifies, so validate refuses one; every other app still needs
// one.
const HostnameNone = "none"

// hostnameInExpression finds a CEL expression that reads the app's hostname
// or its role hostnames, which an app with no hostname has neither of.
var hostnameInExpression = regexp.MustCompile(`\bapp\.hostnames?\b`)

// checkHostless refuses a blueprint whose hostname key says anything but
// none, one that says none in a format before v5, and one that says none and
// declares something that lives on a hostname: the gateway's routing, a sign
// in client, another hostname, the gate, object storage served on a media
// hostname, federation, a link found by the app's own hostname, or a
// provided value made from the hostname.
func (s spec) checkHostless(hasTemplate func(string) bool) error {
	switch s.Hostname {
	case "":
		return nil
	case HostnameNone:
	default:
		return fmt.Errorf("hostname: is %q; the key says the app serves no website, and its one value is %s", s.Hostname, HostnameNone)
	}
	switch s.APIVersion {
	case APIVersionV1, APIVersionV2, APIVersionV3, APIVersionV4:
		return fmt.Errorf("hostname: needs apiVersion %s, the format that adds it, so a toolkit that cannot read it skips the release", APIVersionV5)
	}
	why := "the app has no hostname to serve it on"
	switch {
	case hasTemplate(SnippetFor(PrimaryRole)):
		return fmt.Errorf("hostname: none, and the blueprint ships %s, the gateway's routing for its hostname: %s, so remove it", SnippetFor(PrimaryRole), why)
	case s.OIDC != nil:
		return fmt.Errorf("oidc: a sign in client's callback is on the app's hostname, and %s", why)
	case len(s.HostnameRoles) > 0:
		return fmt.Errorf("hostname_roles: every role is a hostname beside the app's own, and %s", why)
	case s.Gate.Gateable:
		return fmt.Errorf("gate.gateable: the gate stands in front of the app's hostname, and %s", why)
	case s.Storage.S3:
		return fmt.Errorf("storage.s3: stored objects are served on a media hostname derived from the app's, and %s", why)
	case s.Federation.Federates:
		return fmt.Errorf("federation.federates: other servers reach a federating app at its hostname, and %s", why)
	}
	for i, c := range s.Consumes {
		if !c.Optional {
			return fmt.Errorf("consumes[%d].optional: false, and an unset setting then links to the provider at the app's own hostname, but %s; make it optional, so an unset setting links to the deployment's one provider", i, why)
		}
	}
	for i, p := range s.Provides {
		for _, value := range sortedKeys(p.Values) {
			if hostnameInExpression.MatchString(p.Values[value]) {
				return fmt.Errorf("provides[%d].values.%s: reads app.hostname, and %s; offer app.mesh_address instead", i, value, why)
			}
		}
	}
	return nil
}

// Hostless reports whether a blueprint's app serves no website and so takes
// no hostname: hostname: none in its blueprint.yaml.
func Hostless(blueprint config.Blueprint) bool { return of(blueprint).Hostless }
