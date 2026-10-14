package blueprints_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/blueprints"
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/imagedigest"
)

// Mbin and the writefreely-wisp fork keep uploads in S3; Synapse is
// deliberately excluded because its media store is a directory the homeserver
// owns, not a bucket, and Wiki.js keeps its uploads in Postgres. The other
// kinds have no uploads at all.
func TestUsesObjectStorage(t *testing.T) {
	cases := map[config.Kind]bool{
		config.Kind("mbin"):        true,
		config.Kind("wikijs"):      false,
		config.Kind("synapse"):     false,
		config.Kind("element"):     false,
		config.KindOAuth2Proxy:     false,
		config.KindPocketID:        false,
		config.Kind("writefreely"): true,
	}
	for kind, want := range cases {
		if got := blueprints.UsesObjectStorage(kind); got != want {
			t.Errorf("UsesObjectStorage(%s) = %v, want %v", kind, got, want)
		}
	}
}

// Mbin's media must be fetchable by a federating server with no credential,
// and so must the blog's images now that the wisp fork serves them only from
// image_url_base. No other kind's objects are.
func TestMbinAndTheBlogServeObjectsPublicly(t *testing.T) {
	for _, kind := range blueprints.Builtin() {
		want := kind == config.Kind("mbin") || kind == config.Kind("writefreely")
		if got := blueprints.ServesObjectsPublicly(kind); got != want {
			t.Errorf("%s: ServesObjectsPublicly is %v, want %v", kind, got, want)
		}
	}
}

// The wisp fork supports Postgres and S3, so the blog joins the same cluster
// and the same object store as everything else. With S3 the fork is direct
// only: it writes images and never serves them, so a reader with no credential
// has to be able to read its bucket.
func TestWriteFreelyUsesPostgresAndPublicObjectStorage(t *testing.T) {
	if !blueprints.UsesPostgres(config.Kind("writefreely")) {
		t.Error("the wisp fork keeps its data in Postgres")
	}
	if !blueprints.UsesObjectStorage(config.Kind("writefreely")) {
		t.Error("the wisp fork keeps uploaded images in S3")
	}
	if !blueprints.ServesObjectsPublicly(config.Kind("writefreely")) {
		t.Fatal("the fork never serves its own images under S3, so its bucket must be readable by a reader with no credential")
	}
	var sawPostgres bool
	for _, s := range blueprints.Services(config.Kind("writefreely")) {
		if s.Name == blueprints.PostgresService {
			sawPostgres = true
		}
	}
	if !sawPostgres {
		t.Error("a pinned blog runs its own Postgres beside itself, so the kind needs that service")
	}
}

// The media role and object storage are one fact stated twice, and they must
// agree: a kind that stores objects without the role would publish URLs
// nothing routes, and a kind with the role and no bucket would route a
// hostname to nothing.
func TestEveryKindThatStoresObjectsHasAMediaRole(t *testing.T) {
	for _, kind := range blueprints.Builtin() {
		if got, want := blueprints.HasHostnameRole(kind, blueprints.MediaRole), blueprints.UsesObjectStorage(kind); got != want {
			t.Errorf("%s: HasHostnameRole(media) is %v, UsesObjectStorage is %v", kind, got, want)
		}
	}
}

// The derived name is a sibling of the app's own hostname, under the
// community's domain, and never a child of it or a name for the backend. A
// declared name wins outright.
func TestMediaHostnameIsDerivedAsASiblingAndCanBeOverridden(t *testing.T) {
	withGarage := &config.Config{Community: config.Community{Domain: "example.org"}}
	withGarage.Storage.Garage.Sites = []string{"home-a"}
	for _, tc := range []struct {
		name string
		app  config.App
		want string
	}{
		{"mbin", config.App{Kind: config.Kind("mbin"), Hostname: "talk.example.org"}, "talk-media.example.org"},
		{"writefreely", config.App{Kind: config.Kind("writefreely"), Hostname: "blog.example.org"}, "blog-media.example.org"},
		// The label is the first one, and the result is under the domain
		// rather than under whatever the rest of the app's hostname was.
		{"deeper hostname", config.App{Kind: config.Kind("mbin"), Hostname: "talk.eu.example.org"}, "talk-media.example.org"},
		{"declared", config.App{Kind: config.Kind("writefreely"), Hostname: "blog.example.org", Hostnames: map[string]string{"media": "attachments.example.org"}}, "attachments.example.org"},
		{"stores nothing", config.App{Kind: config.KindPocketID, Hostname: "id.example.org"}, ""},
		{"synapse keeps media on disk", config.App{Kind: config.Kind("synapse"), Hostname: "chat.example.org"}, ""},
		// An app keeping its files on its own disk serves them on its own
		// hostname, and has no media hostname to derive or declare.
		{"local storage", config.App{Kind: config.Kind("mbin"), Hostname: "talk.example.org", Storage: config.StorageLocal}, ""},
	} {
		if got := blueprints.MediaHostname(withGarage, tc.app); got != tc.want {
			t.Errorf("%s: MediaHostname = %q, want %q", tc.name, got, tc.want)
		}
	}
	if !blueprints.MediaHostnameIsDerived(withGarage, config.App{Kind: config.Kind("mbin"), Hostname: "talk.example.org"}) {
		t.Error("an app with no hostnames.media has a derived media hostname")
	}
	if blueprints.MediaHostnameIsDerived(withGarage, config.App{Kind: config.Kind("mbin"), Hostname: "talk.example.org", Hostnames: map[string]string{"media": "m.example.org"}}) {
		t.Error("an app that declares hostnames.media does not have a derived one")
	}
}

// Each kind reads its configuration from a different file in a different
// syntax, so a passthrough key means something different per kind. This is a
// fact about the applications rather than a choice, and getting it wrong puts
// a key in a file nothing reads.
func TestEachKindsConfigFileAndFormat(t *testing.T) {
	cases := map[config.Kind]struct {
		format blueprints.Format
		file   string
	}{
		config.Kind("mbin"):        {blueprints.ConfigEnv, ".env"},
		config.Kind("wikijs"):      {blueprints.ConfigEnv, ".env"},
		config.KindPocketID:        {blueprints.ConfigEnv, ".env"},
		config.KindOAuth2Proxy:     {blueprints.ConfigEnv, ".env"},
		config.Kind("writefreely"): {blueprints.ConfigINI, "config.ini"},
		config.Kind("synapse"):     {blueprints.ConfigYAML, "homeserver.yaml"},
		config.Kind("element"):     {blueprints.ConfigJSON, "config.json"},
		config.KindUptime:          {blueprints.ConfigEnv, ".env"},
		config.KindInvite:          {blueprints.ConfigEnv, ".env"},
	}
	for _, kind := range blueprints.Builtin() {
		want, known := cases[kind]
		if !known {
			t.Errorf("%s is not in this table, so nobody decided where its configuration lives", kind)
			continue
		}
		if got := blueprints.ConfigFormat(kind); got != want.format {
			t.Errorf("%s: format is %q, want %q", kind, got, want.format)
		}
		if got := blueprints.ConfigFile(kind); got != want.file {
			t.Errorf("%s: file is %q, want %q", kind, got, want.file)
		}
	}
}

// The two forks the toolkit runs default to a track on the fork's own
// release tags, which carry the fork's suffix, so no upstream build of the
// same version can match.
func TestTheForksDefaultToATrackOnTheirReleases(t *testing.T) {
	for kind, suffix := range map[config.Kind]string{
		config.Kind("mbin"):        "-paisans",
		config.Kind("writefreely"): "-wisp",
	} {
		got, _ := blueprints.DefaultImage(kind, "app")
		if !imagedigest.IsTrack(got) || !strings.HasSuffix(got, suffix) {
			t.Errorf("%s app image is %s, want a track on tags ending %s", kind, got, suffix)
		}
	}
}

func TestInviteKind(t *testing.T) {
	if !blueprints.UsesPostgres(config.KindInvite) {
		t.Fatal("invite keeps its state in Postgres")
	}
	if !blueprints.SendsMail(config.KindInvite) {
		t.Fatal("invite sends mail")
	}
	spec, ok := blueprints.OIDCClient(config.KindInvite, "invite.example.org", "invite")
	if !ok || spec.CallbackURL != "https://invite.example.org/auth/callback" || !spec.PKCE || !spec.ReadsGroups {
		t.Fatalf("%+v", spec)
	}
	if spec.DefaultMemberGroup != blueprints.MembersGroup || spec.DefaultAdminGroup != blueprints.AdminsGroup {
		t.Fatalf("%+v", spec)
	}
	if blueprints.GateFor(config.KindInvite).Gateable {
		t.Fatal("invitees are not members; the gate must never sit in front of invite")
	}
}
