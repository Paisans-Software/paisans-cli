package render

import (
	"github.com/paisans-software/paisans-cli/internal/blueprints"
	"github.com/paisans-software/paisans-cli/internal/config"
)

// SettingOwner is one setting that owns a rendered key, with its blueprint,
// exported so the external tests can check every one is true.
type SettingOwner struct {
	Blueprint config.Blueprint
	blueprints.SettingKey
}

// SettingOwners is every blueprint's settings that own a rendered key.
func SettingOwners() []SettingOwner {
	var out []SettingOwner
	for _, blueprint := range blueprints.Builtin() {
		for _, k := range blueprints.SettingKeys(blueprint) {
			out = append(out, SettingOwner{blueprint, k})
		}
	}
	return out
}

// ComposeSets exposes the compose.yaml scan to a test of its own.
var ComposeSets = composeSets

// ListImages is listImages for files, exported so the external tests can
// hand it compose files no template writes.
func ListImages(cfg *config.Config, secrets *config.Secrets, files []File) (map[string][]ComposeImage, error) {
	p := newPlanner(cfg, secrets)
	if err := p.placeApps(); err != nil {
		return nil, err
	}
	return p.listImages(files)
}

// InfraServices is infraServices, for the test that keeps it the template's.
var InfraServices = infraServices

// JSONQuote is the jsonquote template function.
var JSONQuote = templateFuncs["jsonquote"].(func(string) (string, error))
