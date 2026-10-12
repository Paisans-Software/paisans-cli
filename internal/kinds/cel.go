package kinds

import (
	"fmt"

	"cel.dev/cel-go/cel"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// A blueprint's conditions are CEL expressions (docs/specs/2026-10-12-
// blueprints-and-catalogs.md, "Predicates and checks are CEL"). CEL is typed,
// cannot loop and evaluates without I/O, so a blueprint can add a condition
// without adding code.

// celCostLimit bounds what one evaluation may cost. A condition over an app's
// declaration needs a handful of operations; the limit is there so that a
// blueprint from a catalog cannot make one expensive.
const celCostLimit = 10000

// celEnv is the environment every condition is compiled in. A condition sees
// what the app's declaration decides:
//
//   - app.hostname, its hostname
//   - app.gated and app.auto_login, the two gate booleans
//   - app.federates, whether it answers ActivityPub (Federates)
//   - app.settings, its settings map
//
// Each is declared with its type, so a condition naming a field that does not
// exist, or comparing one with the wrong type, is refused when the blueprint
// is read.
var celEnv = func() *cel.Env {
	env, err := cel.NewEnv(
		cel.Variable("app.hostname", cel.StringType),
		cel.Variable("app.gated", cel.BoolType),
		cel.Variable("app.auto_login", cel.BoolType),
		cel.Variable("app.federates", cel.BoolType),
		cel.Variable("app.settings", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		panic(err)
	}
	return env
}()

// celApp is what a condition sees of an app.
func celApp(app config.App) map[string]any {
	settings := map[string]any{}
	for k, v := range app.Settings {
		settings[k] = v
	}
	return map[string]any{
		"app.hostname":   app.Hostname,
		"app.gated":      app.Gated,
		"app.auto_login": app.AutoLogin,
		"app.federates":  Federates(app),
		"app.settings":   settings,
	}
}

// compileCondition compiles a CEL condition over an app into a function.
//
// A condition that cannot be evaluated for some app, such as one reading a
// setting the app does not declare without asking has() first, holds: every
// condition in a blueprint today decides whether the app must enforce a
// setting, and requiring it is the safe answer to a question that could not
// be asked.
func compileCondition(expr string) (func(config.App) bool, error) {
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
	prg, err := celEnv.Program(ast, cel.CostLimit(celCostLimit))
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
