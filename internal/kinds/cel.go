package kinds

import (
	"fmt"
	"net/mail"
	"regexp"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"

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
//   - app.settings, its settings map
//   - app.smtp, its resolved mail settings (host, port, security, username,
//     from_address, from_name), without the password, empty in a condition
//
// and the functions isBareEmail(string), an email address with nothing
// around it, and isHostname(string). Each variable is declared with its
// type, so an expression naming a field that does not exist, or comparing
// one with the wrong type, is refused when the blueprint is read.
var celEnv = func() *cel.Env {
	env, err := cel.NewEnv(
		cel.Variable("app.hostname", cel.StringType),
		cel.Variable("app.gated", cel.BoolType),
		cel.Variable("app.auto_login", cel.BoolType),
		cel.Variable("app.federates", cel.BoolType),
		cel.Variable("app.placement", cel.StringType),
		cel.Variable("app.settings", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("app.smtp", cel.MapType(cel.StringType, cel.DynType)),
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

// celApp is what an expression sees of an app, smtp being its resolved mail
// settings or nil.
func celApp(app config.App, smtp map[string]any) map[string]any {
	settings := map[string]any{}
	for k, v := range app.Settings {
		settings[k] = v
	}
	if smtp == nil {
		smtp = map[string]any{}
	}
	placement := "cluster"
	if app.Placement.Mode == config.PlacementPinned {
		placement = app.Placement.Site
	}
	return map[string]any{
		"app.hostname":   app.Hostname,
		"app.gated":      app.Gated,
		"app.auto_login": app.AutoLogin,
		"app.federates":  Federates(app),
		"app.placement":  placement,
		"app.settings":   settings,
		"app.smtp":       smtp,
	}
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
func compile(expr string) (cel.Program, error) {
	if expr == "" {
		return nil, fmt.Errorf("required")
	}
	ast, issues := celEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("%q is %s, not a boolean", expr, ast.OutputType())
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
		out, _, err := prg.Eval(celApp(app, nil))
		if err != nil {
			return true
		}
		v, ok := out.Value().(bool)
		return !ok || v
	}, nil
}
