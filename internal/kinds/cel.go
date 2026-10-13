package kinds

import (
	"fmt"
	"net/mail"
	"regexp"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// A blueprint's conditions and checks are CEL expressions (docs/specs/
// 2026-10-12-blueprints-and-catalogs.md, "Predicates and checks are CEL").
// CEL is typed, cannot loop and evaluates without I/O, so a blueprint can add
// a condition without adding code.

// celCostLimit bounds what one evaluation may cost. A condition over an app's
// declaration needs a handful of operations; the limit is there so that a
// blueprint from a catalog cannot make one expensive.
const celCostLimit = 10000

// celEnv is the environment every condition and check is compiled in. An
// expression sees what the app's declaration decides:
//
//   - app.hostname, its hostname
//   - app.gated and app.auto_login, the two gate booleans
//   - app.federates, whether it answers ActivityPub (Federates)
//   - app.placement, "cluster" or the site it is pinned to
//   - app.settings, its settings map, with its blueprint's defaults for
//     the keys it leaves out when the blueprint is from a catalog
//     (SettingsSeen)
//   - app.smtp, its resolved mail settings (host, port, security, username,
//     from_address, from_name), without the password, empty in a condition
//   - app.name, its name under apps, empty in a condition
//   - app.oidc, whether its blueprint signs members in through the identity
//     provider
//   - app.own_smtp, the mail settings the app itself declares, beside the
//     deployment's, which is how a check says which key a value came from,
//     empty in a condition
//   - app.sites, the sites it runs on, empty in a condition
//   - app.storage, where its files go: mode ("object" or "local"), bucket
//     (its bucket's name, when in object storage) and media_hostname (the
//     hostname its objects are served from, when it has one), empty in a
//     condition
//   - deployment.sites, each site as {name, roles}, in name order, empty in
//     a condition
//
// and the functions isBareEmail(string), an email address with nothing
// around it, and isHostname(string), with CEL's string extension (join,
// split, replace, lowerAscii and the rest), which a message expression
// builds its text with. Each variable is declared with its
// type, so an expression naming a field that does not exist, or comparing
// one with the wrong type, is refused when the blueprint is read.
var celEnv = func() *cel.Env {
	env, err := cel.NewEnv(
		ext.Strings(),
		cel.Variable("app.hostname", cel.StringType),
		cel.Variable("app.gated", cel.BoolType),
		cel.Variable("app.auto_login", cel.BoolType),
		cel.Variable("app.federates", cel.BoolType),
		cel.Variable("app.placement", cel.StringType),
		cel.Variable("app.settings", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("app.smtp", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("app.name", cel.StringType),
		cel.Variable("app.oidc", cel.BoolType),
		cel.Variable("app.own_smtp", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("app.sites", cel.ListType(cel.StringType)),
		cel.Variable("app.storage", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("deployment.sites", cel.ListType(cel.MapType(cel.StringType, cel.DynType))),
		cel.Function("isBareEmail", cel.Overload("isBareEmail_string", []*cel.Type{cel.StringType}, cel.BoolType,
			cel.UnaryBinding(func(v ref.Val) ref.Val {
				s, _ := v.Value().(string)
				addr, err := mail.ParseAddress(s)
				return types.Bool(err == nil && addr.Address == s)
			}))),
		cel.Function("isHostname", cel.Overload("isHostname_string", []*cel.Type{cel.StringType}, cel.BoolType,
			cel.UnaryBinding(func(v ref.Val) ref.Val {
				s, _ := v.Value().(string)
				return types.Bool(hostnameRE.MatchString(s))
			}))),
	)
	if err != nil {
		panic(err)
	}
	return env
}()

var hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// celApp is what an expression sees of an app with no deployment around it,
// as a condition sees it.
func celApp(app config.App) map[string]any {
	settings := map[string]any{}
	for k, v := range SettingsSeen(app) {
		settings[k] = v
	}
	placement := "cluster"
	if app.Placement.Mode == config.PlacementPinned {
		placement = app.Placement.Site
	}
	return map[string]any{
		"app.hostname":     app.Hostname,
		"app.gated":        app.Gated,
		"app.auto_login":   app.AutoLogin,
		"app.federates":    Federates(app),
		"app.placement":    placement,
		"app.settings":     settings,
		"app.smtp":         map[string]any{},
		"app.name":         "",
		"app.oidc":         of(app.Kind).OIDC != nil,
		"app.own_smtp":     map[string]any{},
		"app.sites":        []string{},
		"app.storage":      map[string]string{},
		"deployment.sites": []map[string]any{},
	}
}

// celAppIn is what an expression sees of the app called name in cfg, as a
// check sees it.
func celAppIn(cfg *config.Config, name string) map[string]any {
	app := cfg.Apps[name]
	vars := celApp(app)
	vars["app.smtp"] = smtpOf(cfg.SMTPFor(name))
	if app.SMTP != nil {
		vars["app.own_smtp"] = smtpOf(*app.SMTP)
	}
	vars["app.name"] = name
	sites := []string{}
	if app.Placement.Mode == config.PlacementPinned {
		sites = append(sites, app.Placement.Site)
	} else {
		sites = append(sites, cfg.AppsSites()...)
	}
	vars["app.sites"] = sites
	storage := map[string]string{"mode": "local"}
	if InObjectStorage(cfg, app) {
		storage["mode"] = "object"
		storage["bucket"] = BucketName(name, app)
	}
	if media := MediaHostname(cfg, app); media != "" {
		storage["media_hostname"] = media
	}
	vars["app.storage"] = storage
	deployment := []map[string]any{}
	for _, site := range cfg.SiteNames() {
		roles := []string{}
		for _, r := range cfg.Sites[site].Roles {
			roles = append(roles, string(r))
		}
		deployment = append(deployment, map[string]any{"name": site, "roles": roles})
	}
	vars["deployment.sites"] = deployment
	return vars
}

// smtpOf is an app's resolved mail settings as an expression sees them: only
// what is set, so has(app.smtp.host) asks whether there is a mail server.
func smtpOf(s config.SMTP) map[string]any {
	out := map[string]any{}
	for k, v := range map[string]string{"host": s.Host, "security": s.Security, "username": s.Username, "from_address": s.FromAddress, "from_name": s.FromName} {
		if v != "" {
			out[k] = v
		}
	}
	if s.Port != 0 {
		out["port"] = int64(s.Port)
	}
	return out
}

// compile compiles a boolean CEL expression.
func compile(expr string) (cel.Program, error) { return compileOf(expr, cel.BoolType, "a boolean") }

// compileString compiles a CEL expression whose value is a string.
func compileString(expr string) (cel.Program, error) {
	return compileOf(expr, cel.StringType, "a string")
}

func compileOf(expr string, want *cel.Type, what string) (cel.Program, error) {
	if expr == "" {
		return nil, fmt.Errorf("required")
	}
	ast, issues := celEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	if !ast.OutputType().IsExactType(want) {
		return nil, fmt.Errorf("%q is %s, not %s", expr, ast.OutputType(), what)
	}
	return celEnv.Program(ast, cel.CostLimit(celCostLimit))
}

// compileCondition compiles a CEL condition over an app into a function.
//
// A condition that cannot be evaluated for some app, such as one reading a
// setting the app does not declare without asking has() first, holds: every
// condition in a blueprint today decides whether the app must enforce a
// setting, and requiring it is the safe answer to a question that could not
// be asked.
func compileCondition(expr string) (func(config.App) bool, error) {
	prg, err := compile(expr)
	if err != nil {
		return nil, err
	}
	return func(app config.App) bool {
		out, _, err := prg.Eval(celApp(app))
		if err != nil {
			return true
		}
		v, ok := out.Value().(bool)
		return !ok || v
	}, nil
}
