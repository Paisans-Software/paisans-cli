package blueprints

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// APIVersion is the newest blueprint format this toolkit reads, and
// APIVersionV2 and APIVersionV1 the ones before it, read as well. v2 is v1
// with heartbeat, and v3 is v2 whose templates may read .DisplayName and
// .CommunityName. A blueprint that uses either names its format, so a toolkit
// that cannot read it refuses the format by name and uses an older catalog
// release.
const (
	APIVersion   = "paisans.blueprint/v3"
	APIVersionV2 = "paisans.blueprint/v2"
	APIVersionV1 = "paisans.blueprint/v1"
)

// spec is a blueprint.yaml as written. Parse turns it into a Def. Every key
// is checked: one this toolkit does not know is refused by name rather than
// ignored, because a blueprint author's typo would otherwise render an app
// without the thing they asked for.
type spec struct {
	APIVersion      string                 `yaml:"apiVersion"`
	Name            string                 `yaml:"name"`
	RequiresToolkit string                 `yaml:"requires_toolkit"`
	Services        []Service              `yaml:"services"`
	Database        databaseSpec           `yaml:"database"`
	Port            int                    `yaml:"port"`
	Config          configSpec             `yaml:"config"`
	Health          healthSpec             `yaml:"health"`
	Gate            gateSpec               `yaml:"gate"`
	Federation      federation             `yaml:"federation"`
	HostnameRoles   []string               `yaml:"hostname_roles"`
	Storage         storageSpec            `yaml:"storage"`
	Mail            string                 `yaml:"mail"`
	OIDC            *oidcSpec              `yaml:"oidc"`
	Secrets         secretsSpec            `yaml:"secrets"`
	Backup          *BackupSpec            `yaml:"backup"`
	Checks          []checkSpec            `yaml:"checks"`
	Settings        map[string]SettingSpec `yaml:"settings"`
	Provides        []Provide              `yaml:"provides"`
	Consumes        []Consume              `yaml:"consumes"`
	Heartbeat       *heartbeatSpec         `yaml:"heartbeat"`
}

type databaseSpec struct {
	Engine string   `yaml:"engine"`
	Extra  []string `yaml:"extra"`
}

type configSpec struct {
	Format         Format       `yaml:"format"`
	File           string       `yaml:"file"`
	SettingKeys    []SettingKey `yaml:"setting_keys"`
	OmittedKeys    []OmittedKey `yaml:"omitted_keys"`
	LockedSettings []lockedSpec `yaml:"locked_settings"`
}

type healthSpec struct {
	Path   string `yaml:"path"`
	Status []int  `yaml:"status"`
	TCP    bool   `yaml:"tcp"`
}

type probeSpec struct {
	Path   string `yaml:"path"`
	Status []int  `yaml:"status"`
}

type signedFetchSpec struct {
	Settings []string `yaml:"settings"`
	Probe    string   `yaml:"probe"`
	Status   []int    `yaml:"status"`
	Requires string   `yaml:"requires"`
}

type autoLoginSpec struct {
	Paths         []string `yaml:"paths"`
	Target        string   `yaml:"target"`
	AbsentCookies []string `yaml:"absent_cookies"`
}

type gateSettingSpec struct {
	Name      string `yaml:"name"`
	Want      string `yaml:"want"`
	When      string `yaml:"when"`
	Why       string `yaml:"why"`
	ConfigKey string `yaml:"config_key"`
	Setting   string `yaml:"setting"`
}

type gateSpec struct {
	Gateable     bool              `yaml:"gateable"`
	OpenPaths    []string          `yaml:"open_paths"`
	InboxPaths   []string          `yaml:"inbox_paths"`
	TokenPaths   []string          `yaml:"token_paths"`
	AutoLogin    []autoLoginSpec   `yaml:"auto_login"`
	SignInPath   string            `yaml:"sign_in_path"`
	PrivateProbe *probeSpec        `yaml:"private_probe"`
	SignedFetch  *signedFetchSpec  `yaml:"signed_fetch"`
	Settings     []gateSettingSpec `yaml:"settings"`
	Live         *liveSpec         `yaml:"live"`
	LeavesOpen   []string          `yaml:"leaves_open"`
}

type federation struct {
	Federates bool   `yaml:"federates"`
	Setting   string `yaml:"setting"`
}

type storageSpec struct {
	S3              bool     `yaml:"s3"`
	PublicReads     bool     `yaml:"public_reads"`
	LocalOnly       bool     `yaml:"local_only"`
	LocalOnlyReason string   `yaml:"local_only_reason"`
	LocalMediaPaths []string `yaml:"local_media_paths"`
}

type oidcSpec struct {
	// Callback is the redirect URI's path on the app's own hostname. It is a
	// template, given .Hostname and .App and the helper stableULID, for a
	// blueprint whose path depends on the app.
	Callback           string `yaml:"callback"`
	NoLaunch           bool   `yaml:"no_launch"`
	LaunchPath         string `yaml:"launch_path"`
	PKCE               bool   `yaml:"pkce"`
	AdminGroupSetting  string `yaml:"admin_group_setting"`
	MemberGroupSetting string `yaml:"member_group_setting"`
	DefaultAdminGroup  string `yaml:"default_admin_group"`
	DefaultMemberGroup string `yaml:"default_member_group"`
	ReadsGroups        bool   `yaml:"reads_groups"`
	// ExtraGroupsSetting names a list setting whose groups apply finds or
	// creates at the identity provider beside the member and admin groups.
	ExtraGroupsSetting string `yaml:"extra_groups_setting"`
}

type secretsSpec struct {
	Generate    []secretSpec     `yaml:"generate"`
	Required    []RequiredSecret `yaml:"required"`
	SelfRotated []string         `yaml:"self_rotated"`
}

// engines are the database engines a blueprint may name. Only postgres can
// join the cluster; the rest can only be pinned.
var engines = map[string]bool{"postgres": true, "mysql": true, "sqlite": true, "none": true}

// knownRoles are the hostname roles beyond the primary that the gateway can
// route: each selects a snippet, caddy.snippet.<role>.tmpl, from the
// blueprint's templates.
var knownRoles = map[string]bool{MediaRole: true, WellknownRole: true, JoinRole: true}

// callbackHelpers are the functions an OIDC callback template may call.
var callbackHelpers = template.FuncMap{"stableULID": StableULID}

// Parse reads one blueprint.yaml, from the directory named name, into a Def.
// hasTemplate reports whether the blueprint ships a template, so a hostname
// role whose snippet is missing is refused here rather than at render.
func Parse(name string, content []byte, hasTemplate func(string) bool) (*Def, error) {
	var s spec
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("blueprint %s: %w", name, err)
	}
	d, err := s.def(name, hasTemplate)
	if err != nil {
		return nil, fmt.Errorf("blueprint %s: %w", name, err)
	}
	return d, nil
}

func (s spec) def(name string, hasTemplate func(string) bool) (*Def, error) {
	if s.APIVersion != APIVersion && s.APIVersion != APIVersionV2 && s.APIVersion != APIVersionV1 {
		return nil, fmt.Errorf("apiVersion: is %q, and this toolkit reads %s, %s and %s", s.APIVersion, APIVersionV1, APIVersionV2, APIVersion)
	}
	if s.Name != name {
		return nil, fmt.Errorf("name: is %q, but the blueprint's directory is %s", s.Name, name)
	}
	if len(s.Services) == 0 || s.Services[0].Name != "app" {
		return nil, fmt.Errorf("services: the first service must be app, which every template and command names")
	}
	if !engines[s.Database.Engine] {
		return nil, fmt.Errorf("database.engine: is %q; it must be one of postgres, mysql, sqlite or none", s.Database.Engine)
	}
	hasPostgres := false
	for _, svc := range s.Services {
		hasPostgres = hasPostgres || svc.Name == PostgresService
	}
	if hasPostgres != (s.Database.Engine == "postgres") {
		return nil, fmt.Errorf("database.engine: is %s, so a %q service must be listed exactly when it is postgres", s.Database.Engine, PostgresService)
	}
	if s.Port <= 0 || s.Port > 65535 {
		return nil, fmt.Errorf("port: required, the port the app listens on in its container")
	}
	for i, svc := range s.Services {
		if err := checkServiceImage(svc); err != nil {
			return nil, fmt.Errorf("services[%d]: %w", i, err)
		}
	}
	ports := map[int]string{s.Port: "app"}
	for i, svc := range s.Services {
		switch {
		case svc.Port == 0:
			continue
		case i == 0:
			return nil, fmt.Errorf("services[0].port: the app's port is the blueprint's port, not its service's")
		case svc.Port < 0 || svc.Port > 65535:
			return nil, fmt.Errorf("services[%d].port: is %d, not a port", i, svc.Port)
		}
		if other, taken := ports[svc.Port]; taken {
			return nil, fmt.Errorf("services[%d].port: %d is %s's port too", i, svc.Port, other)
		}
		ports[svc.Port] = svc.Name
	}
	for i, extra := range s.Database.Extra {
		if s.Database.Engine != "postgres" {
			return nil, fmt.Errorf("database.extra: only a postgres database keeps others beside it")
		}
		if !config.BlueprintName(extra) || slices.Contains(s.Database.Extra[:i], extra) {
			return nil, fmt.Errorf("database.extra[%d]: %q is not a database name of lowercase letters, digits and hyphens, or is listed twice", i, extra)
		}
	}
	if s.Storage.LocalOnlyReason != "" && !s.Storage.LocalOnly {
		return nil, fmt.Errorf("storage.local_only_reason: says why the files are only local, so it goes with local_only: true")
	}
	switch s.Config.Format {
	case ConfigEnv, ConfigINI, ConfigYAML, ConfigJSON:
	default:
		return nil, fmt.Errorf("config.format: is %q; it must be env, ini, yaml or json", s.Config.Format)
	}
	if s.Config.File == "" {
		return nil, fmt.Errorf("config.file: required")
	}
	var health Health
	switch {
	case s.Health.TCP:
		if s.Health.Path != "" || len(s.Health.Status) > 0 {
			return nil, fmt.Errorf("health: tcp is for an app that serves no HTTP, so it takes no path or status")
		}
		health.TCP = true
	case s.Health.Path == "" && len(s.Health.Status) == 0:
		// No route: the monitor checks only that the app answers.
	default:
		expect, err := statusList(s.Health.Status)
		if err != nil || !strings.HasPrefix(s.Health.Path, "/") {
			return nil, fmt.Errorf("health: needs a path starting with / and at least one status")
		}
		health = Health{Path: s.Health.Path, Expect: expect}
	}
	if err := requirement(s.RequiresToolkit); err != nil {
		return nil, err
	}
	if s.Backup != nil && !needsDump[s.Database.Engine] {
		return nil, fmt.Errorf("backup: only a database the backups cannot read as files is dumped, and %s is not one", s.Database.Engine)
	}
	if b := s.Backup; b != nil && (b.Dump.Service == "" || len(b.Dump.Run) == 0 || b.Restore.Service == "" || len(b.Restore.Run) == 0) {
		return nil, fmt.Errorf("backup: dump and restore each need a service and a command to run")
	}
	var checks []Check
	for i, cs := range s.Checks {
		c, err := cs.check()
		if err != nil {
			return nil, fmt.Errorf("checks[%d]: %w", i, err)
		}
		checks = append(checks, c)
	}
	for key, spec := range s.Settings {
		if !settingTypes[spec.Type] {
			return nil, fmt.Errorf("settings.%s.type: is %q; it must be string, bool, int, email, enum or list", key, spec.Type)
		}
		if (spec.Type == "enum") != (len(spec.Values) > 0) {
			return nil, fmt.Errorf("settings.%s: values go with type enum, and only with it", key)
		}
		// A default is what a template renders for an app that leaves the
		// key out, so one not of the key's own type would render a value
		// validate refuses when an operator writes it.
		if spec.Default != nil {
			if problem := spec.Problem(spec.Default); problem != "" {
				return nil, fmt.Errorf("settings.%s.default: %s", key, problem)
			}
		}
	}
	for _, open := range s.Gate.LeavesOpen {
		if open != "activitypub" && open != "api_tokens" {
			return nil, fmt.Errorf("gate.leaves_open: %q is not something the gate passes through; it passes activitypub and api_tokens", open)
		}
	}
	switch s.Mail {
	case "", "none", "reads":
	default:
		return nil, fmt.Errorf("mail: is %q; it must be reads or none", s.Mail)
	}
	for _, role := range s.HostnameRoles {
		if !knownRoles[role] {
			return nil, fmt.Errorf("hostname_roles: %q is not a role the gateway routes", role)
		}
		if !hasTemplate(SnippetFor(role)) {
			return nil, fmt.Errorf("hostname_roles: %s needs a template %s, which the blueprint does not ship", role, SnippetFor(role))
		}
	}
	provides, err := checkLinks(s.Provides, s.Consumes)
	if err != nil {
		return nil, err
	}
	secrets, err := secretSpecs(s.Secrets.Generate, s.Services)
	if err != nil {
		return nil, err
	}
	heartbeat, token, err := s.Heartbeat.def(s.APIVersion, secrets)
	if err != nil {
		return nil, err
	}
	if token != nil {
		secrets = append(secrets, *token)
	}
	var generate []string
	for _, sec := range secrets {
		generate = append(generate, sec.Name)
	}
	locked, err := lockedSettings(s.Config.LockedSettings)
	if err != nil {
		return nil, err
	}
	gate, settings, live, err := s.Gate.def()
	if err != nil {
		return nil, err
	}
	oidc, err := s.OIDC.def()
	if err != nil {
		return nil, err
	}
	d := &Def{
		Services:        s.Services,
		Port:            s.Port,
		ConfigFormat:    s.Config.Format,
		ConfigFile:      s.Config.File,
		SettingKeys:     s.Config.SettingKeys,
		OmittedKeys:     s.Config.OmittedKeys,
		Health:          health,
		Engine:          s.Database.Engine,
		Backup:          s.Backup,
		Checks:          checks,
		Settings:        s.Settings,
		RequiresToolkit: s.RequiresToolkit,
		Gate:            gate,
		GateSettings:    settings,
		LiveSettings:    live,
		Federates:       s.Federation.Federates,
		HostnameRoles:   s.HostnameRoles,
		ObjectStorage:   s.Storage.S3,
		PublicObjects:   s.Storage.PublicReads,
		LocalFilesOnly:  s.Storage.LocalOnly,
		LocalOnlyReason: s.Storage.LocalOnlyReason,
		ExtraDatabases:  s.Database.Extra,
		LockedSettings:  locked,
		Provides:        provides,
		Consumes:        s.Consumes,
		LocalMediaPaths: s.Storage.LocalMediaPaths,
		SendsMail:       s.Mail == "reads",
		OIDC:            oidc,
		AppSecrets:      generate,
		Secrets:         secrets,
		RequiredSecrets: s.Secrets.Required,
		SelfRotated:     s.Secrets.SelfRotated,
		Heartbeat:       heartbeat,
	}
	d.FederationSetting = s.Federation.Setting
	if s.OIDC != nil {
		d.DashboardPath = s.OIDC.LaunchPath
	}
	return d, nil
}

// statusList is a list of status codes in the uptime fork's syntax: comma
// separated exact codes, because the fork has no ranges.
func statusList(codes []int) (string, error) {
	if len(codes) == 0 {
		return "", fmt.Errorf("no status")
	}
	out := make([]string, len(codes))
	for i, c := range codes {
		if c < 100 || c > 599 {
			return "", fmt.Errorf("%d is not an HTTP status", c)
		}
		out[i] = strconv.Itoa(c)
	}
	return strings.Join(out, ","), nil
}

func (g gateSpec) def() (GateSpec, []GateSetting, LiveSettings, error) {
	out := GateSpec{
		Gateable:   g.Gateable,
		LeavesOpen: g.LeavesOpen,
		OpenPaths:  g.OpenPaths,
		InboxPaths: g.InboxPaths,
		TokenPaths: g.TokenPaths,
		SignInPath: g.SignInPath,
	}
	for _, a := range g.AutoLogin {
		out.AutoLogin = append(out.AutoLogin, AutoLoginRule{Paths: a.Paths, Target: a.Target, AbsentCookies: a.AbsentCookies})
	}
	if p := g.PrivateProbe; p != nil {
		status, err := statusList(p.Status)
		if err != nil {
			return GateSpec{}, nil, nil, fmt.Errorf("gate.private_probe.status: %w", err)
		}
		out.PrivateProbe = &Probe{Path: p.Path, Expect: status}
	}
	if sf := g.SignedFetch; sf != nil {
		status, err := statusList(sf.Status)
		if err != nil {
			return GateSpec{}, nil, nil, fmt.Errorf("gate.signed_fetch.status: %w", err)
		}
		out.SignedFetch = &SignedFetch{Settings: sf.Settings, Probe: sf.Probe, Expect: status, Requires: sf.Requires}
	}
	var settings []GateSetting
	for i, s := range g.Settings {
		when, err := compileCondition(s.When)
		if err != nil {
			return GateSpec{}, nil, nil, fmt.Errorf("gate.settings[%d].when: %w", i, err)
		}
		settings = append(settings, GateSetting{
			Name: s.Name, Want: s.Want, When: when, Why: s.Why,
			ConfigKey: s.ConfigKey, SettingKey: s.Setting,
		})
	}
	var live LiveSettings
	if g.Live != nil {
		l, err := liveAction(*g.Live)
		if err != nil {
			return GateSpec{}, nil, nil, err
		}
		live = l
	}
	if (len(settings) > 0) != (live != nil) {
		return GateSpec{}, nil, nil, fmt.Errorf("gate: settings and live come together, so doctor can read back what the gate needs")
	}
	return out, settings, live, nil
}

func (o *oidcSpec) def() (*OIDCDef, error) {
	if o == nil {
		return nil, nil
	}
	tmpl, err := template.New("callback").Funcs(callbackHelpers).Option("missingkey=error").Parse(o.Callback)
	if err != nil {
		return nil, fmt.Errorf("oidc.callback: %w", err)
	}
	callback := func(hostname, app string) (string, error) {
		var b strings.Builder
		if err := tmpl.Execute(&b, struct{ Hostname, App string }{hostname, app}); err != nil {
			return "", err
		}
		return b.String(), nil
	}
	// Render once with sample values, so a callback that does not render a
	// path is refused when the blueprint is read, not at the end of a sign in.
	sample, err := callback("app.example.org", "app")
	if err != nil {
		return nil, fmt.Errorf("oidc.callback: %w", err)
	}
	if !strings.HasPrefix(sample, "/") {
		return nil, fmt.Errorf("oidc.callback: is %q, which is not a path starting with /", o.Callback)
	}
	if o.LaunchPath != "" && !strings.HasPrefix(o.LaunchPath, "/") {
		return nil, fmt.Errorf("oidc.launch_path: is %q, which is not a path starting with /", o.LaunchPath)
	}
	return &OIDCDef{
		Callback: func(hostname, app string) string {
			path, _ := callback(hostname, app)
			return "https://" + hostname + path
		},
		NoLaunch:           o.NoLaunch,
		PKCE:               o.PKCE,
		AdminGroupSetting:  o.AdminGroupSetting,
		MemberGroupSetting: o.MemberGroupSetting,
		DefaultAdminGroup:  o.DefaultAdminGroup,
		DefaultMemberGroup: o.DefaultMemberGroup,
		ReadsGroups:        o.ReadsGroups,
		ExtraGroupsSetting: o.ExtraGroupsSetting,
	}, nil
}

// reservedExtras is what a reserved blueprint's record gets from the Go that
// drives it, beyond its blueprint.yaml. Only a reserved blueprint has any:
// that Go is in the binary, released with the blueprint.
var reservedExtras = map[config.Blueprint]func(*Def){
	// Every setting in PocketIDSettings owns its variable, taken from that
	// table rather than listed again in the blueprint, so a setting added
	// there is an owner without a second edit.
	config.BlueprintPocketID: func(d *Def) {
		for _, s := range PocketIDSettings {
			d.SettingKeys = append(d.SettingKeys, SettingKey{Key: s.Env, File: ".env", Setting: s.Key})
		}
	},
}

// checkServiceImage checks what a service runs: an image, a track or a
// reference to one build, or a foundation image, never both.
func checkServiceImage(svc Service) error {
	switch {
	case svc.Image != "" && svc.Foundation != "":
		return fmt.Errorf("names both image and foundation; a service runs one")
	case svc.Foundation != "":
		if !knownFoundationName(svc.Foundation) {
			return fmt.Errorf("foundation: %q is not an image the foundation names", svc.Foundation)
		}
	case svc.Image != "":
		if _, err := imageTag(svc.Image); err != nil {
			return fmt.Errorf("image: %w", err)
		}
	}
	return nil
}
