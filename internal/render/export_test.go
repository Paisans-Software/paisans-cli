package render

import (
	"github.com/paisans-software/paisans-stack/internal/config"
	"github.com/paisans-software/paisans-stack/internal/kinds"
)

// SettingOwner is one setting that owns a rendered key, with its kind,
// exported so the external tests can check every one is true.
type SettingOwner struct {
	Kind config.Kind
	kinds.SettingKey
}

// SettingOwners is every kind's settings that own a rendered key.
func SettingOwners() []SettingOwner {
	var out []SettingOwner
	for _, kind := range kinds.Builtin() {
		for _, k := range kinds.SettingKeys(kind) {
			out = append(out, SettingOwner{kind, k})
		}
	}
	return out
}

// ComposeSets exposes the compose.yaml scan to a test of its own.
var ComposeSets = composeSets
