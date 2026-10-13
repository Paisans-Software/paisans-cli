package kinds

import (
	"fmt"
	"net/mail"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"

	"cel.dev/cel-go/cel"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// BackupSpec is how to dump and restore a database the backups do not reach
// as files: a command run in one of the app's own services, its output the
// dump, and the command that reads a dump back on its input.
type BackupSpec struct {
	Dump    InAppCommand `yaml:"dump"`
	Restore InAppCommand `yaml:"restore"`
}

// InAppCommand is a command run inside one of the app's own containers.
type InAppCommand struct {
	Service string   `yaml:"service"`
	Run     []string `yaml:"run"`
}

var ruleID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// needsDump are the engines whose data the backups cannot read as files, so
// a blueprint naming one must say how to dump it.
var needsDump = map[string]bool{"mysql": true}

// Check is one of a blueprint's own validate rules.
type Check struct {
	// ID names the rule in every finding, as validate's own rules are named.
	ID string
	// Refuse is whether a failure refuses the configuration, rather than
	// warning about it.
	Refuse bool
	// Hint is the finding's one line summary, empty for the general one.
	Hint string
	// Key is the app key the finding names, relative to apps.<app>.
	Key string
	// Message is what the operator reads when the rule fails, when the
	// blueprint gives it as text.
	Message string
	prg     cel.Program
	// message and key, when set, work the message and the key out for the
	// app that fails, so either can name what is wrong.
	message, key cel.Program
}

// checkSpec is a check as a blueprint writes it.
type checkSpec struct {
	ID          string `yaml:"id"`
	Level       string `yaml:"level"`
	Hint        string `yaml:"hint"`
	Key         string `yaml:"key"`
	KeyExpr     string `yaml:"key_expr"`
	Rule        string `yaml:"rule"`
	Message     string `yaml:"message"`
	MessageExpr string `yaml:"message_expr"`
}

func (s checkSpec) check() (Check, error) {
	if !ruleID.MatchString(s.ID) || (s.Message == "") == (s.MessageExpr == "") {
		return Check{}, fmt.Errorf("needs an id, lowercase words joined by hyphens, and one of message and message_expr")
	}
	if s.Key != "" && s.KeyExpr != "" {
		return Check{}, fmt.Errorf("gives both key and key_expr; give one")
	}
	if s.Level != "refuse" && s.Level != "warn" {
		return Check{}, fmt.Errorf("level: is %q; it must be refuse or warn", s.Level)
	}
	prg, err := compile(s.Rule)
	if err != nil {
		return Check{}, fmt.Errorf("rule: %w", err)
	}
	c := Check{ID: s.ID, Refuse: s.Level == "refuse", Hint: s.Hint, Key: s.Key, Message: s.Message, prg: prg}
	if s.MessageExpr != "" {
		if c.message, err = compileString(s.MessageExpr); err != nil {
			return Check{}, fmt.Errorf("message_expr: %w", err)
		}
	}
	if s.KeyExpr != "" {
		if c.key, err = compileString(s.KeyExpr); err != nil {
			return Check{}, fmt.Errorf("key_expr: %w", err)
		}
	}
	return c, nil
}

// Holds evaluates the check for the app named name. A rule that cannot be
// evaluated, such as one reading a setting the app does not declare without
// asking has() first, fails: the blueprint's author asked for something the
// toolkit could not confirm.
func (c Check) Holds(cfg *config.Config, name string) bool {
	out, _, err := c.prg.Eval(celAppIn(cfg, name))
	if err != nil {
		return false
	}
	v, ok := out.Value().(bool)
	return ok && v
}

// Failure is the key, relative to apps.<name>, and the message of the check
// failing for the app called name. A key or message expression that cannot
// be worked out for the app gives the check's own key, or says so.
func (c Check) Failure(cfg *config.Config, name string) (key, message string) {
	key, message = c.Key, c.Message
	vars := celAppIn(cfg, name)
	eval := func(prg cel.Program) (string, bool) {
		out, _, err := prg.Eval(vars)
		if err != nil {
			return "", false
		}
		v, ok := out.Value().(string)
		return v, ok
	}
	if c.key != nil {
		if v, ok := eval(c.key); ok {
			key = v
		}
	}
	if c.message != nil {
		v, ok := eval(c.message)
		if !ok {
			v = "fails, and its blueprint's message for it could not be worked out"
		}
		message = v
	}
	return key, message
}

// ChecksFor is the checks of an app's kind.
func ChecksFor(kind config.Kind) []Check { return append([]Check(nil), of(kind).Checks...) }

// EngineOf is a kind's database engine.
func EngineOf(kind config.Kind) string { return of(kind).Engine }

// BackupOf is how a kind's database is dumped, nil for one with no dump.
func BackupOf(kind config.Kind) *BackupSpec { return of(kind).Backup }

// NeedsDump reports whether a kind's engine is one the backups cannot read
// as files.
func NeedsDump(kind config.Kind) bool { return needsDump[of(kind).Engine] }

// SettingSpec is the type of one `settings` key a blueprint reads: string,
// bool, int, email, or enum with its values. Default, when set, is the value
// the blueprint's templates and expressions see for an app that leaves the
// key out (see SettingsSeen); nil means the key has none.
type SettingSpec struct {
	Type    string   `yaml:"type"`
	Values  []string `yaml:"values"`
	Default any      `yaml:"default"`
}

var settingTypes = map[string]bool{"string": true, "bool": true, "int": true, "email": true, "enum": true}

// SettingsOf is the settings a kind declares, nil for one that declares
// none.
func SettingsOf(kind config.Kind) map[string]SettingSpec { return of(kind).Settings }

// SettingsSeen is the settings map an app's blueprint templates and CEL
// expressions read: the app's own settings, and for a blueprint read from a
// catalog, the default of every declared key the app leaves out. A setting
// the app declares always wins over the default.
//
// Defaults are filled here, for what the blueprint itself reads, and never
// into config.App.Settings or the configuration. The built in blueprints'
// Go code and validate's rules give "not set" a meaning of its own (Eg:
// AuthorizedFetch treats an absent authorized_fetch as on, and the
// passthrough checks look at which settings an app declares), so a default
// written into the configuration would change those answers without anyone
// asking. A built in blueprint gets its own settings back unchanged.
func SettingsSeen(app config.App) map[string]any {
	if _, fromCatalog := Source(app.Kind); !fromCatalog {
		return app.Settings
	}
	var out map[string]any
	for key, spec := range SettingsOf(app.Kind) {
		if _, set := app.Settings[key]; set || spec.Default == nil {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(app.Settings)+1)
			for k, v := range app.Settings {
				out[k] = v
			}
		}
		out[key] = spec.Default
	}
	if out == nil {
		return app.Settings
	}
	return out
}

// Problem is why a value is not one of the setting's type, or empty.
func (s SettingSpec) Problem(v any) string {
	switch s.Type {
	case "bool":
		if _, ok := v.(bool); !ok {
			return fmt.Sprintf("is %v, not true or false", v)
		}
	case "int":
		if _, ok := v.(int); !ok {
			return fmt.Sprintf("is %v, not a whole number", v)
		}
	case "email", "string", "enum":
		str, ok := v.(string)
		if !ok {
			return fmt.Sprintf("is %v, not a string", v)
		}
		if addr, err := mail.ParseAddress(str); s.Type == "email" && (err != nil || addr.Address != str) {
			return fmt.Sprintf("is %q, not an email address alone", str)
		}
		if s.Type == "enum" && !contains(s.Values, str) {
			return fmt.Sprintf("is %q; it must be one of %s", str, strings.Join(s.Values, ", "))
		}
	}
	return ""
}

// requirement checks a blueprint's requires_toolkit against this binary. The
// requirement is ">= MAJOR.MINOR[.PATCH]". A binary built from a release tag
// must be at least that release; one built from a commit between releases,
// or without version information, is a development build and meets it.
func requirement(req string) error {
	if req == "" {
		return nil
	}
	want, ok := strings.CutPrefix(strings.TrimSpace(req), ">=")
	if !ok {
		return fmt.Errorf("requires_toolkit: is %q; write it as >= MAJOR.MINOR", req)
	}
	need, err := parseVersion(strings.TrimSpace(want))
	if err != nil {
		return fmt.Errorf("requires_toolkit: %w", err)
	}
	have, release := toolkitRelease()
	if release && less(have, need) {
		return fmt.Errorf("requires_toolkit: needs paisans-stack %s, and this is %d.%d.%d", strings.TrimSpace(want), have[0], have[1], have[2])
	}
	return nil
}

func parseVersion(v string) ([3]int, error) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, fmt.Errorf("%q is not MAJOR.MINOR or MAJOR.MINOR.PATCH", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, fmt.Errorf("%q is not MAJOR.MINOR or MAJOR.MINOR.PATCH", v)
		}
		out[i] = n
	}
	return out, nil
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// toolkitRelease is this binary's release version, and false for a
// development build.
func toolkitRelease() ([3]int, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return [3]int{}, false
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" || strings.Contains(v, "-") {
		return [3]int{}, false
	}
	out, err := parseVersion(v)
	return out, err == nil
}
