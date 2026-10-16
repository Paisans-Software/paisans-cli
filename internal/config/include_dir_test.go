package config_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// include_dir reads as a path or false; true names nothing and is refused.
func TestIncludeDirReadsAPathOrFalse(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  config.IncludeDir
		fails bool
	}{
		{"/opt/caddy/paisans.d", config.IncludeDir{Path: "/opt/caddy/paisans.d"}, false},
		{"false", config.IncludeDir{Off: true}, false},
		{"true", config.IncludeDir{}, true},
		{"[a, b]", config.IncludeDir{}, true},
	} {
		cfg, err := loadWebServer(t, tc.value)
		if (err != nil) != tc.fails {
			t.Errorf("%s: err %v", tc.value, err)
			continue
		}
		if err == nil && cfg.Sites["watch"].WebServer.IncludeDir != tc.want {
			t.Errorf("%s: got %+v", tc.value, cfg.Sites["watch"].WebServer.IncludeDir)
		}
	}
}

func loadWebServer(t *testing.T, includeDir string) (*config.Config, error) {
	t.Helper()
	var in config.WebServer
	if err := yaml.Unmarshal([]byte("managed_by: external\ninclude_dir: "+includeDir+"\n"), &in); err != nil {
		return nil, err
	}
	return &config.Config{Sites: map[string]config.Site{"watch": {WebServer: &in}}}, nil
}
