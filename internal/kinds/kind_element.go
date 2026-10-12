package kinds

// elementDef is the element kind.
var elementDef = Def{
	Services: []Service{
		// No database: Element is a static client that talks to a homeserver
		// the browser reaches directly.
		//
		// v1.12.27 checked against ghcr.io on 2026-09-15 by requesting the
		// manifest for that exact tag, which resolved (200, an OCI image
		// index). The registry's tag list was paged through in full and
		// v1.12.27 is the newest non release candidate tag; v1.12.28-rc.1
		// exists but is a release candidate, not a release.
		{Name: "app", Image: "ghcr.io/element-hq/element-web:v1.12.27", Purpose: "the web client"},
	},
	Port:         80,
	ConfigFormat: ConfigJSON,
	ConfigFile:   "config.json",
	SettingKeys: []SettingKey{
		{Key: "brand", File: "config.json", Setting: "brand"},
	},
	// The static client has no health route; /version is a cheap file the
	// image serves. Ran v1.12.27: /version 200, /health 404.
	Health: Health{"/version", "200"},
	Gate:   GateSpec{Gateable: true},
}
