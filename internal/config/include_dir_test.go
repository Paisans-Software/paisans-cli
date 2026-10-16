package config_test

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/paisans-software/paisans-cli/internal/config"
)

// include_dir is not a setting: whatever it holds, it loads, so that
// validate's refusal (web-server-include-dir) is the one message about it.
func TestIncludeDirLoadsWhateverItHolds(t *testing.T) {
	for _, value := range []string{"/opt/caddy/paisans.d", "false", "true", "[a, b]", "{a: b}"} {
		cfg, err := loadWebServer(t, value)
		if err != nil {
			t.Errorf("%s: err %v", value, err)
			continue
		}
		if cfg.Sites["watch"].WebServer.IncludeDir.Unset() {
			t.Errorf("%s: read as unset", value)
		}
	}
	var unset config.WebServer
	if err := yaml.Unmarshal([]byte("managed_by: external\n"), &unset); err != nil || !unset.IncludeDir.Unset() {
		t.Errorf("no include_dir: %+v, %v", unset.IncludeDir, err)
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
