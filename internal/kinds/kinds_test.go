package kinds_test

import (
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// Mbin, Outline and the writefreely-wisp fork keep uploads in S3; Synapse is
// deliberately excluded because its media store is a directory the homeserver
// owns, not a bucket. The other kinds have no uploads at all.
func TestUsesObjectStorage(t *testing.T) {
	cases := map[config.Kind]bool{
		config.Kind("mbin"):        true,
		config.Kind("outline"):     true,
		config.Kind("synapse"):     false,
		config.Kind("element"):     false,
		config.KindOAuth2Proxy:     false,
		config.KindPocketID:        false,
		config.Kind("writefreely"): true,
	}
	for kind, want := range cases {
		if got := kinds.UsesObjectStorage(kind); got != want {
			t.Errorf("UsesObjectStorage(%s) = %v, want %v", kind, got, want)
		}
	}
}

// Mbin's media must be fetchable by a federating server with no credential,
// and so must the blog's images now that the wisp fork serves them only from
// image_url_base. Outline's bucket holds document attachments and must never
// be.
func TestMbinAndTheBlogServeObjectsPubliclyAndOutlineDoesNot(t *testing.T) {
	for _, kind := range kinds.Builtin() {
		want := kind == config.Kind("mbin") || kind == config.Kind("writefreely")
		if got := kinds.ServesObjectsPublicly(kind); got != want {
			t.Errorf("%s: ServesObjectsPublicly is %v, want %v", kind, got, want)
		}
	}
	if kinds.ServesObjectsPublicly(config.Kind("outline")) {
		t.Fatal("outline's bucket holds document attachments and must never be world readable")
	}
}

// The wisp fork supports Postgres and S3, so the blog joins the same cluster
// and the same object store as everything else. With S3 the fork is direct
// only: it writes images and never serves them, so a reader with no credential
// has to be able to read its bucket.
func TestWriteFreelyUsesPostgresAndPublicObjectStorage(t *testing.T) {
	if !kinds.UsesPostgres(config.Kind("writefreely")) {
		t.Error("the wisp fork keeps its data in Postgres")
	}
	if !kinds.UsesObjectStorage(config.Kind("writefreely")) {
		t.Error("the wisp fork keeps uploaded images in S3")
	}
	if !kinds.ServesObjectsPublicly(config.Kind("writefreely")) {
		t.Fatal("the fork never serves its own images under S3, so its bucket must be readable by a reader with no credential")
	}
	var sawPostgres bool
	for _, s := range kinds.Services(config.Kind("writefreely")) {
		if s.Name == kinds.PostgresService {
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
	for _, kind := range kinds.Builtin() {
		if got, want := kinds.HasHostnameRole(kind, kinds.MediaRole), kinds.UsesObjectStorage(kind); got != want {
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
		{"outline", config.App{Kind: config.Kind("outline"), Hostname: "docs.example.org"}, "docs-media.example.org"},
		{"writefreely", config.App{Kind: config.Kind("writefreely"), Hostname: "blog.example.org"}, "blog-media.example.org"},
		// The label is the first one, and the result is under the domain
		// rather than under whatever the rest of the app's hostname was.
		{"deeper hostname", config.App{Kind: config.Kind("mbin"), Hostname: "talk.eu.example.org"}, "talk-media.example.org"},
		{"declared", config.App{Kind: config.Kind("outline"), Hostname: "docs.example.org", Hostnames: map[string]string{"media": "attachments.example.org"}}, "attachments.example.org"},
		{"stores nothing", config.App{Kind: config.KindPocketID, Hostname: "id.example.org"}, ""},
		{"synapse keeps media on disk", config.App{Kind: config.Kind("synapse"), Hostname: "chat.example.org"}, ""},
		// An app keeping its files on its own disk serves them on its own
		// hostname, and has no media hostname to derive or declare.
		{"local storage", config.App{Kind: config.Kind("mbin"), Hostname: "talk.example.org", Storage: config.StorageLocal}, ""},
	} {
		if got := kinds.MediaHostname(withGarage, tc.app); got != tc.want {
			t.Errorf("%s: MediaHostname = %q, want %q", tc.name, got, tc.want)
		}
	}
	if !kinds.MediaHostnameIsDerived(withGarage, config.App{Kind: config.Kind("mbin"), Hostname: "talk.example.org"}) {
		t.Error("an app with no hostnames.media has a derived media hostname")
	}
	if kinds.MediaHostnameIsDerived(withGarage, config.App{Kind: config.Kind("mbin"), Hostname: "talk.example.org", Hostnames: map[string]string{"media": "m.example.org"}}) {
		t.Error("an app that declares hostnames.media does not have a derived one")
	}
}

// Each kind reads its configuration from a different file in a different
// syntax, so a passthrough key means something different per kind. This is a
// fact about the applications rather than a choice, and getting it wrong puts
// a key in a file nothing reads.
func TestEachKindsConfigFileAndFormat(t *testing.T) {
	cases := map[config.Kind]struct {
		format kinds.Format
		file   string
	}{
		config.Kind("mbin"):        {kinds.ConfigEnv, ".env"},
		config.Kind("outline"):     {kinds.ConfigEnv, ".env"},
		config.KindPocketID:        {kinds.ConfigEnv, ".env"},
		config.KindOAuth2Proxy:     {kinds.ConfigEnv, ".env"},
		config.Kind("writefreely"): {kinds.ConfigINI, "config.ini"},
		config.Kind("synapse"):     {kinds.ConfigYAML, "homeserver.yaml"},
		config.Kind("element"):     {kinds.ConfigJSON, "config.json"},
		config.KindUptime:          {kinds.ConfigEnv, ".env"},
		config.KindInvite:          {kinds.ConfigEnv, ".env"},
	}
	for _, kind := range kinds.Builtin() {
		want, known := cases[kind]
		if !known {
			t.Errorf("%s is not in this table, so nobody decided where its configuration lives", kind)
			continue
		}
		if got := kinds.ConfigFormat(kind); got != want.format {
			t.Errorf("%s: format is %q, want %q", kind, got, want.format)
		}
		if got := kinds.ConfigFile(kind); got != want.file {
			t.Errorf("%s: file is %q, want %q", kind, got, want.file)
		}
	}
}

// The two forks the toolkit runs default to a release of each, named by its
// version tag, so a toolkit release renders for the fork build it was tested
// against.
func TestTheForksDefaultToAReleaseVersion(t *testing.T) {
	for kind, want := range map[config.Kind]string{
		config.Kind("mbin"):        "ghcr.io/paisans-software/mbin:1.15.1-paisans",
		config.Kind("writefreely"): "ghcr.io/josephquigley/writefreely-wisp:0.21.2-wisp",
	} {
		if got, _ := kinds.DefaultImage(kind, "app"); got != want {
			t.Errorf("%s app image is %s, want %s", kind, got, want)
		}
	}
}

func TestInviteKind(t *testing.T) {
	if !kinds.UsesPostgres(config.KindInvite) {
		t.Fatal("invite keeps its state in Postgres")
	}
	if !kinds.SendsMail(config.KindInvite) {
		t.Fatal("invite sends mail")
	}
	spec, ok := kinds.OIDCClient(config.KindInvite, "invite.example.org", "invite")
	if !ok || spec.CallbackURL != "https://invite.example.org/auth/callback" || !spec.PKCE || !spec.ReadsGroups {
		t.Fatalf("%+v", spec)
	}
	if spec.DefaultMemberGroup != kinds.MembersGroup || spec.DefaultAdminGroup != kinds.AdminsGroup {
		t.Fatalf("%+v", spec)
	}
	if kinds.GateFor(config.KindInvite).Gateable {
		t.Fatal("invitees are not members; the gate must never sit in front of invite")
	}
}
