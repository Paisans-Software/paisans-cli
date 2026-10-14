// Package blueprints records what the toolkit knows about each application it
// can render: a blueprint's blueprint.yaml parsed into a Def, with the
// services its compose template defines and the image each of those services
// runs unless the configuration says otherwise.
//
// It exists as its own package because two callers need the same answer and
// neither should own it. internal/validate refuses an `images` key that names
// a service the blueprint does not have, and internal/render resolves a
// declared reference against the default. If either kept its own copy they
// would drift, and the drift would show up as a configuration that validates
// and then renders a compose file with a service nobody declared.
//
// The records come from three places: the reserved blueprints built into the
// binary (embed.go), the core catalog's newest release, and the catalogs a
// configuration adds. See docs/specs/2026-10-12-blueprints-and-catalogs.md.
package blueprints

import (
	"sort"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// Service is one container in a kind's compose template.
type Service struct {
	// Name is the compose service name, which is also the key an operator
	// writes under `images`.
	Name string
	// Image is the reference this service runs when the configuration does not
	// override it: a track (imagedigest.IsTrack), which apply resolves to the
	// newest build on it, or a reference to one build. An empty value means
	// the default is computed rather than fixed: postgres follows
	// cluster.postgres_version, and a service naming Foundation takes the
	// foundation's image.
	Image string
	// Foundation is the name of the core catalog's foundation image the
	// service runs (FoundationPocketID and the rest), for a reserved
	// blueprint, whose images are the toolkit's own. Services and
	// DefaultImage fill Image from it.
	Foundation string
	// Purpose is one line, quoted back when an operator names a service that
	// does not exist so the message can list what does.
	Purpose string
	// Port is where a service other than app listens, for one the gateway
	// routes to directly: it is published on the site's mesh address, given
	// upstreams of its own and checked for collisions as the app's port is.
	// Zero for a service only its own stack reaches.
	Port int
}

// ServicePorts is the services of a kind that listen on a port of their
// own, in compose order.
func ServicePorts(blueprint config.Blueprint) []Service {
	var out []Service
	for _, svc := range of(blueprint).Services {
		if svc.Port != 0 {
			out = append(out, svc)
		}
	}
	return out
}

// ExtraDatabases is the names of the databases a kind keeps beside its
// own, each <database>_<name> in the same Postgres.
func ExtraDatabases(blueprint config.Blueprint) []string {
	return append([]string(nil), of(blueprint).ExtraDatabases...)
}

// LocalOnlyReason is why a kind that keeps its files only on its own disk
// has no object storage form, as its blueprint says, empty when it does not.
func LocalOnlyReason(blueprint config.Blueprint) string { return of(blueprint).LocalOnlyReason }

// PostgresService is the database container a pinned app runs beside itself. A
// clustered app has no such service: it connects to the local HAProxy, so
// declaring an image for it renders nothing. That is a warning rather than a
// refusal, because the declaration is harmless and an app's placement can
// change.
const PostgresService = "postgres"

// WALGImage is the repository of the Postgres image every pinned Postgres
// runs: Debian's postgres:<major>-trixie plus WAL-G, built by this
// repository (docs/specs/2026-10-11-backups.md, "Pinned Postgres").
const WALGImage = "ghcr.io/paisans-software/postgres-walg"

// SendsMail reports whether a kind reads the deployment's smtp settings. An
// app level smtp block on any other kind is refused, because an override
// nothing reads is ignored without a word.
func SendsMail(blueprint config.Blueprint) bool { return of(blueprint).SendsMail }

// Services returns a kind's services in compose order, which is the order they
// are declared in the kind's record rather than alphabetical: `app` first is what an operator
// expects to read.
func Services(blueprint config.Blueprint) []Service {
	out := append([]Service(nil), of(blueprint).Services...)
	for i := range out {
		if out[i].Foundation != "" {
			// A foundation that cannot be read leaves Image empty, and
			// ServiceImage says why.
			out[i].Image, _ = FoundationImage(out[i].Foundation)
		}
	}
	return out
}

// ServiceImage is the image a kind's service runs when the configuration
// says nothing, with why when the foundation that names it cannot be read.
// Empty with no error for a default computed elsewhere (postgres).
func ServiceImage(s Service) (string, error) {
	if s.Foundation != "" {
		return FoundationImage(s.Foundation)
	}
	return s.Image, nil
}

// Has reports whether a kind defines a service.
func Has(blueprint config.Blueprint, service string) bool {
	for _, s := range of(blueprint).Services {
		if s.Name == service {
			return true
		}
	}
	return false
}

// DefaultImage returns the reference a service runs when the configuration
// says nothing. The second result is false when the kind computes it instead
// of shipping one.
func DefaultImage(blueprint config.Blueprint, service string) (string, bool) {
	for _, s := range Services(blueprint) {
		if s.Name == service {
			return s.Image, s.Image != ""
		}
	}
	return "", false
}

// UsesPostgres reports whether a kind stores its data in Postgres at all.
//
// Two kinds do not: Element, which is a static client with no server side
// state at all, and oauth2-proxy, which keeps a session in a cookie. The
// difference is load bearing rather than cosmetic in two directions. An app
// that uses no Postgres needs no database role, no password and no place in
// the cluster, so requiring one would refuse a configuration that is entirely
// correct. It is also what cluster-placement-without-a-cluster reads: a kind
// answering false here can only be pinned, because cluster placement would
// render it onto every apps site with storage of its own.
func UsesPostgres(blueprint config.Blueprint) bool {
	return Has(blueprint, PostgresService)
}

// UsesObjectStorage reports whether a kind can keep its uploads in S3 rather
// than on local disk. Whether one app of the kind does is InObjectStorage,
// which also reads the app's `storage` key and whether the deployment has a
// Garage at all.
func UsesObjectStorage(blueprint config.Blueprint) bool { return of(blueprint).ObjectStorage }

// ServesObjectsPublicly reports whether a kind's objects must be readable
// without a credential. Each kind's record says why its answer is what it is.
//
// This is deliberately not a configuration key. Garage's website access is per
// bucket and opt in, so the only way a private bucket becomes world readable
// is a change to a kind's record, which is a code change with a review rather
// than a line somebody edits at two in the morning.
//
// A kind storing objects (UsesObjectStorage) is not the same question as one
// whose objects are fetched anonymously: a kind can keep its uploads in S3
// while serving them through its own application route, or through
// signatures it issues, never exposing its bucket.
func ServesObjectsPublicly(blueprint config.Blueprint) bool { return of(blueprint).PublicObjects }

// ServiceNames returns a kind's service names, sorted, for an error message
// that has to list them.
func ServiceNames(blueprint config.Blueprint) []string {
	var out []string
	for _, s := range of(blueprint).Services {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// Format is the syntax of the file a kind reads its configuration from.
type Format string

const (
	// ConfigNone is the zero value: a kind that renders no configuration file.
	// Every kind today renders one, so nothing returns it yet; it is declared so
	// that a kind added later without one has an honest answer to give.
	ConfigNone Format = ""
	ConfigEnv  Format = "env"
	ConfigINI  Format = "ini"
	ConfigYAML Format = "yaml"
	ConfigJSON Format = "json"
)

// ConfigFormat is the syntax of the file a kind reads its configuration from,
// or ConfigNone when it has none.
func ConfigFormat(blueprint config.Blueprint) Format { return of(blueprint).ConfigFormat }

// ConfigFile is the rendered path of that file, relative to the stack.
func ConfigFile(blueprint config.Blueprint) string { return of(blueprint).ConfigFile }

// BucketName is the bucket an app that stores objects keeps them in: the
// `s3_bucket` setting when one is declared, and the app's name with
// "-uploads" otherwise.
//
// It is here because three packages need the same answer: the renderer, which
// gives the app the name; the provisioner, which creates the bucket; and
// validate, which checks the name against what the app will do with it. Three
// copies of one default is how the bucket the app writes to and the bucket the
// provisioner created stop being the same bucket.
func BucketName(name string, app config.App) string {
	if v, ok := app.Settings["s3_bucket"].(string); ok && v != "" {
		return v
	}
	return name + "-uploads"
}
