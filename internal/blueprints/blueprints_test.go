package blueprints_test

import (
	"strings"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/imagedigest"
)

// Mbin and the writefreely-wisp fork keep uploads in S3; Synapse is
// deliberately excluded because its media store is a directory the homeserver
// owns, not a bucket, and Wiki.js keeps its uploads in Postgres. The other
// blueprints have no uploads at all.
func TestUsesObjectStorage(t *testing.T) {
	cases := map[config.Blueprint]bool{
		config.Blueprint("mbin"):        true,
		config.Blueprint("wikijs"):      false,
		config.Blueprint("synapse"):     false,
		config.Blueprint("element"):     false,
		config.BlueprintOAuth2Proxy:     false,
		config.BlueprintPocketID:        false,
		config.Blueprint("writefreely"): true,
	}
	for blueprint, want := range cases {
		if got := blueprints.UsesObjectStorage(blueprint); got != want {
			t.Errorf("UsesObjectStorage(%s) = %v, want %v", blueprint, got, want)
		}
	}
}

// Mbin's media must be fetchable by a federating server with no credential,
// and so must the blog's images now that the wisp fork serves them only from
// image_url_base. No other blueprint's objects are.
func TestMbinAndTheBlogServeObjectsPublicly(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		want := blueprint == config.Blueprint("mbin") || blueprint == config.Blueprint("writefreely")
		if got := blueprints.ServesObjectsPublicly(blueprint); got != want {
			t.Errorf("%s: ServesObjectsPublicly is %v, want %v", blueprint, got, want)
		}
	}
}

// The wisp fork supports Postgres and S3, so the blog joins the same cluster
// and the same object store as everything else. With S3 the fork is direct
// only: it writes images and never serves them, so a reader with no credential
// has to be able to read its bucket.
func TestWriteFreelyUsesPostgresAndPublicObjectStorage(t *testing.T) {
	if !blueprints.UsesPostgres(config.Blueprint("writefreely")) {
		t.Error("the wisp fork keeps its data in Postgres")
	}
	if !blueprints.UsesObjectStorage(config.Blueprint("writefreely")) {
		t.Error("the wisp fork keeps uploaded images in S3")
	}
	if !blueprints.ServesObjectsPublicly(config.Blueprint("writefreely")) {
		t.Fatal("the fork never serves its own images under S3, so its bucket must be readable by a reader with no credential")
	}
	var sawPostgres bool
	for _, s := range blueprints.Services(config.Blueprint("writefreely")) {
		if s.Name == blueprints.PostgresService {
			sawPostgres = true
		}
	}
	if !sawPostgres {
		t.Error("a pinned blog runs its own Postgres beside itself, so the blueprint needs that service")
	}
}

// The media role and object storage are one fact stated twice, and they must
// agree: a blueprint that stores objects without the role would publish URLs
// nothing routes, and a blueprint with the role and no bucket would route a
// hostname to nothing.
func TestEveryBlueprintThatStoresObjectsHasAMediaRole(t *testing.T) {
	for _, blueprint := range blueprints.Builtin() {
		if got, want := blueprints.HasHostnameRole(blueprint, blueprints.MediaRole), blueprints.UsesObjectStorage(blueprint); got != want {
			t.Errorf("%s: HasHostnameRole(media) is %v, UsesObjectStorage is %v", blueprint, got, want)
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
		{"mbin", config.App{Blueprint: config.Blueprint("mbin"), Hostname: "talk.example.org"}, "talk-media.example.org"},
		{"writefreely", config.App{Blueprint: config.Blueprint("writefreely"), Hostname: "blog.example.org"}, "blog-media.example.org"},
		// The label is the first one, and the result is under the domain
		// rather than under whatever the rest of the app's hostname was.
		{"deeper hostname", config.App{Blueprint: config.Blueprint("mbin"), Hostname: "talk.eu.example.org"}, "talk-media.example.org"},
		{"declared", config.App{Blueprint: config.Blueprint("writefreely"), Hostname: "blog.example.org", Hostnames: map[string]string{"media": "attachments.example.org"}}, "attachments.example.org"},
		{"stores nothing", config.App{Blueprint: config.BlueprintPocketID, Hostname: "id.example.org"}, ""},
		{"synapse keeps media on disk", config.App{Blueprint: config.Blueprint("synapse"), Hostname: "chat.example.org"}, ""},
		// An app keeping its files on its own disk serves them on its own
		// hostname, and has no media hostname to derive or declare.
		{"local storage", config.App{Blueprint: config.Blueprint("mbin"), Hostname: "talk.example.org", Storage: config.StorageLocal}, ""},
	} {
		if got := blueprints.MediaHostname(withGarage, tc.app); got != tc.want {
			t.Errorf("%s: MediaHostname = %q, want %q", tc.name, got, tc.want)
		}
	}
	if !blueprints.MediaHostnameIsDerived(withGarage, config.App{Blueprint: config.Blueprint("mbin"), Hostname: "talk.example.org"}) {
		t.Error("an app with no hostnames.media has a derived media hostname")
	}
	if blueprints.MediaHostnameIsDerived(withGarage, config.App{Blueprint: config.Blueprint("mbin"), Hostname: "talk.example.org", Hostnames: map[string]string{"media": "m.example.org"}}) {
		t.Error("an app that declares hostnames.media does not have a derived one")
	}
}

// Each blueprint reads its configuration from a different file in a different
// syntax, so a passthrough key means something different per blueprint. This is
// a fact about the applications rather than a choice, and getting it wrong puts
// a key in a file nothing reads.
func TestEachBlueprintsConfigFileAndFormat(t *testing.T) {
	cases := map[config.Blueprint]struct {
		format blueprints.Format
		file   string
	}{
		config.Blueprint("mbin"):        {blueprints.ConfigEnv, ".env"},
		config.Blueprint("wikijs"):      {blueprints.ConfigEnv, ".env"},
		config.BlueprintPocketID:        {blueprints.ConfigEnv, ".env"},
		config.BlueprintOAuth2Proxy:     {blueprints.ConfigEnv, ".env"},
		config.Blueprint("writefreely"): {blueprints.ConfigINI, "config.ini"},
		config.Blueprint("synapse"):     {blueprints.ConfigYAML, "homeserver.yaml"},
		config.Blueprint("element"):     {blueprints.ConfigJSON, "config.json"},
		config.BlueprintUptime:          {blueprints.ConfigEnv, ".env"},
		config.BlueprintAccount:         {blueprints.ConfigEnv, ".env"},
		config.BlueprintEngagement:      {blueprints.ConfigEnv, ".env"},
		config.BlueprintBeszel:          {blueprints.ConfigEnv, ".env"},
		config.BlueprintOpsNotifier:     {blueprints.ConfigEnv, ".env"},
	}
	for _, blueprint := range blueprints.Builtin() {
		want, known := cases[blueprint]
		if !known {
			t.Errorf("%s is not in this table, so nobody decided where its configuration lives", blueprint)
			continue
		}
		if got := blueprints.ConfigFormat(blueprint); got != want.format {
			t.Errorf("%s: format is %q, want %q", blueprint, got, want.format)
		}
		if got := blueprints.ConfigFile(blueprint); got != want.file {
			t.Errorf("%s: file is %q, want %q", blueprint, got, want.file)
		}
	}
}

// The two forks the toolkit runs default to a track on the fork's own
// release tags, which carry the fork's suffix, so no upstream build of the
// same version can match.
func TestTheForksDefaultToATrackOnTheirReleases(t *testing.T) {
	for blueprint, suffix := range map[config.Blueprint]string{
		config.Blueprint("mbin"):        "-paisans",
		config.Blueprint("writefreely"): "-wisp",
	} {
		got, _ := blueprints.DefaultImage(blueprint, "app")
		if !imagedigest.IsTrack(got) || !strings.HasSuffix(got, suffix) {
			t.Errorf("%s app image is %s, want a track on tags ending %s", blueprint, got, suffix)
		}
	}
}

func TestAccountBlueprint(t *testing.T) {
	if !blueprints.UsesPostgres(config.BlueprintAccount) {
		t.Fatal("account keeps its state in Postgres")
	}
	if !blueprints.SendsMail(config.BlueprintAccount) {
		t.Fatal("account sends mail")
	}
	spec, ok := blueprints.OIDCClient(config.BlueprintAccount, "account.example.org", "account")
	if !ok || spec.CallbackURL != "https://account.example.org/auth/callback" || !spec.PKCE || !spec.ReadsGroups {
		t.Fatalf("%+v", spec)
	}
	if spec.DefaultMemberGroup != blueprints.MembersGroup || spec.DefaultAdminGroup != blueprints.AdminsGroup {
		t.Fatalf("%+v", spec)
	}
	if blueprints.GateFor(config.BlueprintAccount).Gateable {
		t.Fatal("people signing up are not members; the gate must never sit in front of account")
	}
}
