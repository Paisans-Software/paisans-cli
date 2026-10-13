package kinds

// MbinDashboardPath is where the identity provider's dashboard tile for an
// Mbin app sends a member. The bare host lands them signed out, with a log in
// button still to press; this route, oauth_oidc_connect in
// config/mbin_routes/security.yaml at tag v1.13.3+paisans, starts the OIDC
// flow, so picking the tile signs the member in.
const MbinDashboardPath = "/oauth/oidc/connect"

// MbinContactEmailSetting is the app setting naming the admins' address, which
// Mbin renders as KBIN_CONTACT_EMAIL: where its /contact form sends each
// message, the address /api/info publishes, and the subject of its Web Push
// VAPID token. When it is absent the sender address takes its place, so a
// message reaches whoever reads the sending mailbox. README, "Mbin sends
// little mail, and says where from", has the reasoning.
const MbinContactEmailSetting = "contact_email"

// MbinQueueSetting is the app setting that picks where an Mbin app's
// Messenger queues live: MbinQueuePostgres, the default, or MbinQueueRabbitMQ.
// README, "Mbin's queues are in Postgres by default", has the reasoning.
const MbinQueueSetting = "queue"

const (
	// MbinQueuePostgres keeps the queues as rows in the app's own database,
	// through Symfony Messenger's Doctrine transport. They survive losing an
	// apps site, and every site's consumers share them. It needs paisans fork
	// 1.14.0-paisans or later, which carries the Doctrine transport decorator:
	// on an older image the shipped messenger.yaml's AMQP options are refused
	// by the Doctrine transport at the first dispatch.
	MbinQueuePostgres = "postgres"
	// MbinQueueRabbitMQ runs a broker per stack, behind amqproxy, as upstream
	// does. Faster under load, and lost with its site.
	MbinQueueRabbitMQ = "rabbitmq"
)

// MbinQueue is the queue backend an Mbin app's settings choose, the default
// when they choose none. A value that is neither is returned as given, for
// validate to refuse by name.
func MbinQueue(settings map[string]any) string {
	if s, ok := settings[MbinQueueSetting].(string); ok && s != "" {
		return s
	}
	return MbinQueuePostgres
}

// MbinPrivateAPIProbe is the read a monitor makes, with no token, of a gated
// Mbin to prove it runs as a private instance. With MBIN_PRIVATE_INSTANCE on,
// the catch-all access rule refuses a request with no signed-in user
// (security.yaml:152, PrivateInstanceVoter.php:30-36 at v1.15.0+paisans), and
// the API firewall's OAuth2 entry point answers that refusal 401
// (league/oauth2-server-bundle v1.1.1, OAuth2Authenticator.php:60-63). The
// entries list is a route that exists whatever content does
// (config/mbin_routes/entry_api.yaml:62), and an instance that is not private
// answers it 200.
const MbinPrivateAPIProbe = "/api/entries"
