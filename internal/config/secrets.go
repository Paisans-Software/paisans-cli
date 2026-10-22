package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	sopsage "github.com/getsops/sops/v3/age"
	sopsyaml "github.com/getsops/sops/v3/stores/yaml"
	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/deployment"
	"github.com/paisans-software/paisans-cli/internal/keystore"
	"github.com/paisans-software/paisans-cli/internal/progname"
	"github.com/paisans-software/paisans-cli/internal/ui"
)

// SecretsVersion is the format of the secrets file this toolkit reads and
// writes. It is the file's own, and changes independently of paisans-stack.yaml's
// version.
//
// Version 2 adds the backup section. Its restic password and WAL-G key
// decrypt every backup taken, and nothing can regenerate them, so a toolkit
// that reads only version 1, and would drop the section on its next write,
// must refuse the file instead. Version 1 is read as it is, and every write
// is at this version.
const SecretsVersion = 2

// Secrets is the decrypted content of secrets.enc.yaml. Nothing here is ever
// written back out except into a rendered artifact.
//
// Three kinds live in one file: generated at init and never seen, pasted
// because someone else issued them, and captured after a running service
// minted them.
type Secrets struct {
	// Version is the file's format, SecretsVersion when written. A file
	// without one is read as this format and written with it.
	Version int `yaml:"version"`
	// ID is the id of the deployment the file belongs to, paisans-stack.yaml's id.
	// The file holds that deployment's credentials and the state captured
	// from its running services, so LoadDeploymentSecrets refuses it for any
	// other. A file without one is accepted and carries the id from its next
	// write.
	ID          string                    `yaml:"id,omitempty"`
	Cluster     ClusterSecrets            `yaml:"cluster"`
	Storage     StorageSecrets            `yaml:"storage"`
	Backup      BackupSecrets             `yaml:"backup,omitempty"`
	Sites       map[string]SiteSecrets    `yaml:"sites"`
	Apps        map[string]map[string]any `yaml:"apps"`
	External    map[string]string         `yaml:"external"`
	OIDCClients map[string]OIDCClient     `yaml:"oidc_clients"`
	// PocketIDGroups is the ID each group named in the Pocket ID app's
	// signup_default_groups has at the deployment's Pocket ID, keyed by
	// group name. apply resolves and records them, as it records a client's
	// ID under oidc_clients, so that render is a pure function of the files
	// and every Pocket ID instance renders the same IDs. Not a secret, but
	// it is state the toolkit wrote about a live instance, which is what
	// this file holds beside the credentials.
	PocketIDGroups map[string]string `yaml:"pocket_id_groups,omitempty"`
	// Operators is what is known of each operator paisans-stack.yaml declares that
	// no host should read in plaintext, keyed by the operator's key there. The
	// configuration is copied to every site in plaintext; this file is
	// copied encrypted to the operators' age keys, which no host holds.
	Operators map[string]OperatorSecrets `yaml:"operators,omitempty"`
	// MeshRekeyRemaining is the sites a `paisans mesh rekey --all` run has
	// left, in order, set only while one is under way: written before the
	// first site's rekey, and each site taken off as its key is promoted, so
	// that a stopped run resumes without rekeying a site it finished.
	MeshRekeyRemaining []string `yaml:"mesh_rekey_remaining,omitempty"`
	// Rotations is the next value of each secret `paisans secrets rotate` is
	// replacing, keyed by the secret's dotted path, set only while a rotation
	// is under way. It is written before any host is handed the value, so a
	// run that stops after the database holds the new password and the apps
	// still the old one finds it and finishes with it rather than generating
	// another. A rotation is pending while its path is here; promotion moves
	// the value to its own place and removes the entry.
	Rotations map[string]string `yaml:"rotations,omitempty"`
	// RotateRemaining is the paths a `paisans secrets rotate --all` run has
	// left, in order, kept for the same reason as MeshRekeyRemaining: a
	// stopped run resumes without rotating a secret it finished.
	RotateRemaining []string `yaml:"rotate_remaining,omitempty"`

	// Path is where these were read from, for error messages.
	Path string `yaml:"-"`
	// Encrypted records whether the file was sops encrypted. A plaintext file
	// is accepted so that tests and examples work without a key, and the
	// caller is expected to say so out loud.
	Encrypted bool `yaml:"-"`
	// UnknownKeys records that the file holds a key this type does not, at
	// any depth, which writing the file back would drop.
	UnknownKeys bool `yaml:"-"`
}

// OperatorSecrets is one declared operator's entry under Secrets.Operators.
type OperatorSecrets struct {
	// DNSProviderEmail is the address the operator's membership at the DNS
	// provider is under, when it is not their email. The DNS continuity
	// check expects them there instead
	// (docs/specs/2026-10-13-dns-admin-continuity.md), and only a keyed hash
	// of it is rendered to a host.
	DNSProviderEmail string `yaml:"dns_provider_email,omitempty"`
}

type ClusterSecrets struct {
	SuperuserPassword string `yaml:"superuser_password"`
	AdminPassword     string `yaml:"admin_password"`
	StandbyPassword   string `yaml:"standby_password"`
}

type StorageSecrets struct {
	Garage GarageSecrets `yaml:"garage"`
}

// GarageSecrets is Garage's own credentials: the admin API token and the RPC
// secret nodes use to talk to each other. S3 credentials are not here. They
// are per app, under Secrets.Apps, because one shared key would let any one
// app read, rewrite and delete every other app's objects.
type GarageSecrets struct {
	AdminToken string `yaml:"admin_token"`
	RPCSecret  string `yaml:"rpc_secret"`
}

// BackupSecrets is what backups need beyond paisans-stack.yaml's backup section:
// two keys generated at init and never seen, and each destination's
// credentials, which its provider issues and an operator pastes.
type BackupSecrets struct {
	// ResticPassword opens the restic repository in every destination.
	ResticPassword string `yaml:"restic_password,omitempty"`
	// WALGLibsodiumKey encrypts every object WAL-G writes, 32 bytes as hex.
	WALGLibsodiumKey string `yaml:"walg_libsodium_key,omitempty"`
	// Destinations is keyed by the destination's name in paisans-stack.yaml.
	Destinations map[string]DestinationSecrets `yaml:"destinations,omitempty"`
}

// DestinationSecrets is the S3 key a destination's provider issued for its
// bucket.
type DestinationSecrets struct {
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
}

type SiteSecrets struct {
	WireGuardPrivateKey string `yaml:"wireguard_private_key"`
	// WireGuardNextPrivateKey is the key `paisans mesh rekey` is moving the
	// site to, set only while a rekey is under way: it is written before any
	// host is handed it, so a run that stops part way finds it and resumes,
	// and it becomes WireGuardPrivateKey once every peer has a handshake with
	// the site under it. Render never reads it. README, "Rekeying the mesh".
	WireGuardNextPrivateKey string `yaml:"wireguard_next_private_key,omitempty"`
	// HeartbeatToken is what the site's host pushes to every monitor with,
	// and what each monitor's seed names for the site: 32 lowercase hex
	// characters, generated at init. README, "Monitoring".
	HeartbeatToken string `yaml:"heartbeat_token"`
}

// RekeyPending is every site with a next WireGuard key, sorted: a `paisans
// mesh rekey` that has not finished. Its host may already run the next key,
// and the file names the old one as current until the rekey promotes it.
func (s *Secrets) RekeyPending() []string {
	var out []string
	for name, site := range s.Sites {
		if site.WireGuardNextPrivateKey != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// RotationPending is the path of every secret with a next value, sorted: a
// `paisans secrets rotate` that has not finished. Hosts may already run the
// next value while the file names the old one as current.
func (s *Secrets) RotationPending() []string {
	out := make([]string, 0, len(s.Rotations))
	for path := range s.Rotations {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

type OIDCClient struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// LoadSecrets reads a secrets file, decrypting it in process when it is sops
// encrypted. The sops and age libraries are embedded deliberately: neither
// binary is a dependency of a workstation, and the fewer install steps stand
// between an operator and a working deployment the better.
//
// Decryption happens here, on a workstation, and never on a host. What that
// buys is blast radius: root on one host yields that host's rendered secrets,
// not another site's.
func LoadSecrets(path string) (*Secrets, error) { return loadSecrets(path, "") }

// loadSecrets is LoadSecrets for the deployment id, whose key decryption
// looks for in the keystore. "" looks nowhere but where sops does.
func loadSecrets(path, id string) (*Secrets, error) {
	s, _, err := loadSecretsBytes(path, id, "")
	return s, err
}

// loadSecretsBytes is loadSecrets, and the bytes it read. replacing is a
// pasted secret's key the pasted secrets check passes over (checkPasted),
// "" for none.
func loadSecretsBytes(path, id, replacing string) (*Secrets, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, readProblem(path, "Run "+progname.Name()+" init to create it, or name the file with --secrets.", err)
	}
	s, err := parseSecrets(data, path, id, replacing)
	return s, data, err
}

// parseSecrets decrypts and decodes data, read from path, and refuses a
// pasted secret holding a control character, except the one at replacing.
func parseSecrets(data []byte, path, id, replacing string) (*Secrets, error) {
	var err error
	plain := data
	encrypted := looksEncrypted(data)
	if encrypted {
		plain, err = decryptSOPS(data, id)
		if err != nil {
			return nil, decryptProblem(path, err)
		}
	}
	var s Secrets
	if err := yaml.Unmarshal(plain, &s); err != nil {
		return nil, parseProblem(path, "a secrets file", err)
	}
	s.Path = path
	s.Encrypted = encrypted
	strict := yaml.NewDecoder(bytes.NewReader(plain))
	strict.KnownFields(true)
	s.UnknownKeys = strict.Decode(&Secrets{}) != nil
	if s.Version < 0 || s.Version > SecretsVersion {
		return nil, &ui.Problem{
			Hint:    fmt.Sprintf("%s is a version %d secrets file", ui.ShortPath(path), s.Version),
			Explain: fmt.Sprintf("It was written by a newer toolkit; this one reads version %d. Use the toolkit release that wrote it, or a later one.", SecretsVersion),
			Cause:   fmt.Errorf("%s: version: expected at most %d, got %d", path, SecretsVersion, s.Version),
		}
	}
	if s.Version == 0 {
		s.Version = SecretsVersion
	}
	if err := s.checkPasted(replacing); err != nil {
		return nil, err
	}
	return &s, nil
}

// LoadDeploymentSecrets is LoadSecrets for the deployment whose id is id,
// and is how every command reads them. A file another deployment wrote is
// refused: its OIDC client credentials and Pocket ID group IDs name objects
// at that deployment's services, and nothing else would notice them used
// here. A file without an id is given this one, which its next write keeps.
// A plaintext file is refused, unless PlaintextSecretsVariable is set to 1
// for a test or a fixture.
func LoadDeploymentSecrets(path, id string) (*Secrets, error) {
	s, _, err := LoadDeploymentSecretsWithBytes(path, id)
	return s, err
}

// LoadDeploymentSecretsWithBytes is LoadDeploymentSecrets, and the bytes it
// read, as they are on disk: what apply publishes as the version it applies
// is then exactly what it loaded.
func LoadDeploymentSecretsWithBytes(path, id string) (*Secrets, []byte, error) {
	return loadDeploymentSecrets(path, id, "")
}

// LoadDeploymentSecretsReplacing is LoadDeploymentSecrets for a command
// about to replace the pasted secret at key: a control character in that
// one value does not refuse the file, so `paisans secrets set` can repair
// it. A control character in any other pasted secret still does.
func LoadDeploymentSecretsReplacing(path, id, key string) (*Secrets, error) {
	s, _, err := loadDeploymentSecrets(path, id, key)
	return s, err
}

func loadDeploymentSecrets(path, id, replacing string) (*Secrets, []byte, error) {
	s, data, err := loadSecretsBytes(path, id, replacing)
	if err != nil {
		return nil, nil, err
	}
	if !s.Encrypted && !PlaintextSecretsAllowed() {
		return nil, nil, PlaintextProblem(path)
	}
	s, err = deploymentSecrets(s, path, id)
	return s, data, err
}

func deploymentSecrets(s *Secrets, path, id string) (*Secrets, error) {
	if s.ID != "" && id != "" && s.ID != id {
		return nil, foreignSecretsProblem(path, s.ID, id)
	}
	if s.ID == "" {
		s.ID = id
	}
	return s, nil
}

// foreignSecretsProblem says that the file at path belongs to deployment
// theirs rather than ours. The ids are shortened to their tokens, the way
// names on a host carry them, unless the tokens are the same.
func foreignSecretsProblem(path, theirs, ours string) error {
	a, b := deployment.Deployment{ID: theirs}.Token()+"…", deployment.Deployment{ID: ours}.Token()+"…"
	if a == b {
		a, b = theirs, ours
	}
	return &ui.Problem{
		Hint:    fmt.Sprintf("%s belongs to deployment %s, not %s", ui.ShortPath(path), a, b),
		Explain: "It holds another deployment's credentials and the state captured from its running services, such as OIDC clients and Pocket ID group IDs, so it is not read for this one. To start this deployment fresh, move that file aside and run " + progname.Name() + " init; " + progname.Name() + " deployment apply creates a new one as well.",
		Cause:   fmt.Errorf("%s: id: %s, and the configuration's id is %s", path, theirs, ours),
	}
}

// AgeKeystore is where decryption looks for a deployment's age key when no
// SOPS_AGE_* variable names one, and where init keeps the key it makes.
// Tests replace it.
var AgeKeystore keystore.Store = keystore.Platform()

// ageVariables are the variables through which an operator names an age key
// to sops. Any one of them set means the operator chose, and the keystore is
// not read.
var ageVariables = []string{"SOPS_AGE_KEY", "SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY_CMD", "SOPS_AGE_SSH_PRIVATE_KEY_FILE", "SOPS_AGE_SSH_PRIVATE_KEY_CMD"}

// AgeVariables returns the SOPS_AGE_* variable names, for a caller that has
// to clear them.
func AgeVariables() []string { return append([]string(nil), ageVariables...) }

// AgeVariableSet returns the first of the SOPS_AGE_* variables that is set,
// and whether there is one. While one is, decryption uses it and not the
// keystore, and init uses its key rather than making one.
func AgeVariableSet() (string, bool) {
	for _, name := range ageVariables {
		if os.Getenv(name) != "" {
			return name, true
		}
	}
	return "", false
}

// errNoKey and errTampered are what decryptSOPS found, for decryptProblem to
// say in an operator's words.
var (
	errNoKey    = errors.New("no age identity could decrypt the data key")
	errTampered = errors.New("authentication code mismatch")
)

// decryptProblem says why path could not be decrypted and what to do. sops'
// own wording is the cause, for --verbose: failing to find a key is the
// ordinary case for a new admin, and "0 successful groups required, got 0"
// tells them nothing they can act on. The embedded sops (v3.13.3,
// age/keysource.go) reads a key from SOPS_AGE_KEY_CMD, SOPS_AGE_KEY_FILE,
// SOPS_AGE_KEY and sops/age/keys.txt under the user's config directory; the
// command comes first, since it lets the key live in a keychain. With a
// deployment id, paisans reads the keystore item for it first; see dataKey.
func decryptProblem(path string, err error) error {
	cause := fmt.Errorf("decrypting %s: %w", path, err)
	name := ui.ShortPath(path)
	const noKey = "Set SOPS_AGE_KEY_CMD to a command that prints your key, Eg: a Keychain lookup, so the key is never stored in a file or the environment. SOPS_AGE_KEY_FILE (its path), SOPS_AGE_KEY (the key itself) and sops/age/keys.txt in your config directory are read too. Your public key must be a recipient in .sops.yaml; if it was added recently, run sops updatekeys on the file."
	var miss *keystoreMiss
	var stale *notRecipient
	var failed *keystoreFailed
	var chosen *variableChosen
	switch {
	case errors.As(err, &failed):
		return &ui.Problem{
			Hint:    name + " cannot be decrypted: the keystore could not be read",
			Explain: fmt.Sprintf("paisans looked for %s and the keystore said: %v. Unlock or repair the keystore and re-run, or name a key another way. %s", failed.item, failed.cause, noKey),
			Cause:   cause,
		}
	case errors.As(err, &chosen):
		return &ui.Problem{
			Hint:    name + " cannot be decrypted: no usable age key was found",
			Explain: fmt.Sprintf("%s %s is set, so %s was not read.", noKey, chosen.name, chosen.item),
			Cause:   cause,
		}
	case errors.As(err, &stale):
		return &ui.Problem{
			Hint:    fmt.Sprintf("the keystore key for deployment %s… is not a recipient of %s", deployment.Deployment{ID: stale.id}.Token(), name),
			Explain: fmt.Sprintf("%s holds a key that opens none of the recipients in %s, and no other key was found. Either the item is from another key, or .sops.yaml changed after the file was encrypted. Add the item's recipient to .sops.yaml and run sops updatekeys %s, or set SOPS_AGE_KEY_CMD to the right key.", stale.item, name, name),
			Cause:   cause,
		}
	case errors.As(err, &miss):
		return &ui.Problem{
			Hint:    name + " cannot be decrypted: no usable age key was found",
			Explain: noKey + " paisans also looked for " + miss.item + " and found none.",
			Cause:   cause,
		}
	case errors.Is(err, errNoKey):
		return &ui.Problem{Hint: name + " cannot be decrypted: no usable age key was found", Explain: noKey, Cause: cause}
	case errors.Is(err, errTampered):
		return &ui.Problem{
			Hint:    name + " was changed after it was encrypted",
			Explain: "Its authentication code does not match its content, so it is not read. Restore it from git rather than editing it by hand.",
			Cause:   cause,
		}
	}
	return &ui.Problem{Hint: name + " cannot be decrypted", Explain: "sops said: " + err.Error() + ".", Cause: cause}
}

// looksEncrypted reports whether a file carries sops metadata. It is a cheap
// check on the serialised form rather than a decode, because a plaintext
// fixture is a legitimate input and must not have to satisfy the sops schema.
func looksEncrypted(data []byte) bool {
	var probe struct {
		SOPS map[string]any `yaml:"sops"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	return len(probe.SOPS) > 0
}

func decryptSOPS(data []byte, id string) ([]byte, error) {
	store := &sopsyaml.Store{}
	tree, err := store.LoadEncryptedFile(data)
	if err != nil {
		return nil, err
	}
	key, err := dataKey(&tree.Metadata, id)
	if err != nil {
		return nil, err
	}
	cipher := aes.NewCipher()
	mac, err := tree.Decrypt(key, cipher)
	if err != nil {
		return nil, err
	}
	if err := verifyMAC(&tree, key, cipher, mac); err != nil {
		return nil, err
	}
	return store.EmitPlainFile(tree.Branches)
}

// dataKey opens the file's data key. With an id and no variable set, the
// key in the keystore item for id is tried first, in memory: nothing is set
// in the environment, so no child paisans starts can inherit it. sops'
// own search runs when there is no item, when the keystore cannot be read,
// and when the item's key is not a recipient, so neither a stale item nor a
// broken keystore ever hides a key sops would find. With a variable set,
// sops' search alone runs, and a keystore item it passed over is reported
// through KeystoreBypassed.
func dataKey(m *sops.Metadata, id string) ([]byte, error) {
	if id == "" {
		return sopsDataKey(m)
	}
	if name, ok := AgeVariableSet(); ok {
		key, err := sopsDataKey(m)
		if _, none := AgeKeystore.(keystore.Unavailable); errors.Is(err, errNoKey) && !none {
			return nil, &variableChosen{name: name, item: AgeKeystore.Describe(id), err: err}
		}
		if err == nil {
			noticeBypass(id)
		}
		return key, err
	}
	identity, err := AgeKeystore.Get(id)
	if errors.Is(err, keystore.ErrUnavailable) {
		return sopsDataKey(m)
	}
	if errors.Is(err, keystore.ErrNotFound) {
		key, err := sopsDataKey(m)
		if errors.Is(err, errNoKey) {
			return nil, &keystoreMiss{item: AgeKeystore.Describe(id), err: err}
		}
		return key, err
	}
	if err != nil {
		// A keystore that is there but cannot be read, Eg: secret-tool with
		// no D-Bus session or a locked Keychain over ssh, must not stop a key
		// sops finds on its own, which is how such a machine worked before.
		key, sopsErr := sopsDataKey(m)
		if errors.Is(sopsErr, errNoKey) {
			return nil, &keystoreFailed{item: AgeKeystore.Describe(id), cause: err, err: sopsErr}
		}
		return key, sopsErr
	}
	if key, ok := keystoreDataKey(m, identity); ok {
		return key, nil
	}
	key, err := sopsDataKey(m)
	if errors.Is(err, errNoKey) {
		return nil, &notRecipient{id: id, item: AgeKeystore.Describe(id), err: err}
	}
	return key, err
}

func sopsDataKey(m *sops.Metadata) ([]byte, error) {
	key, err := m.GetDataKey()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoKey, err)
	}
	return key, nil
}

// keystoreDataKey tries identity against each age recipient of a file with
// one key group, which is every file the toolkit writes. A file with more
// groups splits its data key between them, and is left to sops.
func keystoreDataKey(m *sops.Metadata, identity string) ([]byte, bool) {
	if len(m.KeyGroups) != 1 {
		return nil, false
	}
	var ids sopsage.ParsedIdentities
	if err := ids.Import(identity); err != nil {
		return nil, false
	}
	for _, k := range m.KeyGroups[0] {
		mk, ok := k.(*sopsage.MasterKey)
		if !ok {
			continue
		}
		c := *mk
		ids.ApplyToMasterKey(&c)
		if key, err := c.Decrypt(); err == nil {
			return key, true
		}
	}
	return nil, false
}

// keystoreMiss is no usable key, with no keystore item for the deployment.
type keystoreMiss struct {
	item string
	err  error
}

func (e *keystoreMiss) Error() string { return e.err.Error() }
func (e *keystoreMiss) Unwrap() error { return e.err }

// keystoreFailed is no usable key, with a keystore that could not be read.
// cause is the keystore's own error, which the operator needs to repair it.
type keystoreFailed struct {
	item       string
	cause, err error
}

func (e *keystoreFailed) Error() string {
	return fmt.Sprintf("%v; reading %s: %v", e.err, e.item, e.cause)
}
func (e *keystoreFailed) Unwrap() error { return e.err }

// variableChosen is no usable key while a SOPS_AGE_* variable is set, so
// the keystore item was not read.
type variableChosen struct {
	name, item string
	err        error
}

func (e *variableChosen) Error() string { return e.err.Error() }
func (e *variableChosen) Unwrap() error { return e.err }

// notRecipient is a keystore item whose key opens none of the file's
// recipients, and no other key sops could find.
type notRecipient struct {
	id, item string
	err      error
}

func (e *notRecipient) Error() string { return e.err.Error() }
func (e *notRecipient) Unwrap() error { return e.err }

// verifyMAC confirms the file has not been altered since it was encrypted. A
// decrypt that skips this reads a tampered file happily, and these values
// become a running deployment's credentials.
func verifyMAC(tree *sops.Tree, key []byte, cipher sops.Cipher, computed string) error {
	stored, err := cipher.Decrypt(
		tree.Metadata.MessageAuthenticationCode,
		key,
		tree.Metadata.LastModified.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("cannot read the file's authentication code: %w", err)
	}
	if stored != computed {
		return fmt.Errorf("%w: the file was modified after it was encrypted", errTampered)
	}
	return nil
}
