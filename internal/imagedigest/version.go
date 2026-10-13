package imagedigest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// VersionLabel is the label an image's build records its version in. Both
// forks the toolkit runs set it through docker/metadata-action, and so do
// most images built that way.
const VersionLabel = "org.opencontainers.image.version"

// Version returns the version label of the build ref names, which must carry
// its digest (Eg: ghcr.io/paisans-software/mbin:stack-1@sha256:...), empty
// when the build has no such label. It reads the build's image configuration,
// anonymously as Digest does, for every linux platform an index lists. One
// release labels every platform alike, and that label is the answer; when
// the platforms disagree each is named beside its own (Eg: "1.15.0 (amd64),
// 1.15.1 (arm64)"), so no one platform's version passes for the image's.
//
// It is for the dry run, which names the versions a moved image goes between
// beside its digests. A digest is what pins a build; a label is what a
// publisher wrote in it, and a build without one is not an error.
func (r Registry) Version(ref string) (string, error) {
	builds, err := r.platforms(ref)
	if err != nil {
		return "", err
	}
	versions := map[string]string{}
	for _, b := range builds {
		versions[b.Platform] = b.Labels[VersionLabel]
	}
	return agreed(versions, func(v string) string {
		if v == "" {
			return "none"
		}
		return v
	}), nil
}

// agreed is the one value every platform has, or, when they differ, each
// platform's value as show prints it, followed by the platform, in platform
// order.
func agreed(byPlatform map[string]string, show func(string) string) string {
	names := make([]string, 0, len(byPlatform))
	for name := range byPlatform {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	same := true
	for _, name := range names[1:] {
		if byPlatform[name] != byPlatform[names[0]] {
			same = false
		}
	}
	if same {
		return byPlatform[names[0]]
	}
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s (%s)", show(byPlatform[name]), name)
	}
	return strings.Join(parts, ", ")
}

// Build is what one platform of an image declares in its configuration.
type Build struct {
	// Platform is the architecture, with its variant when the index gives
	// one (Eg: amd64, arm64/v8), and the OS when it is not linux.
	Platform string
	Labels   map[string]string
	// Volumes are the paths its VOLUME instructions declare, sorted.
	Volumes []string
}

// platforms reads the configuration of every platform of the build ref
// names by digest: each linux entry of an index, skipping the attestations
// an index lists beside them, or the one manifest of an image published for
// a single platform.
func (r Registry) platforms(ref string) ([]Build, error) {
	at := strings.LastIndex(ref, "@")
	if at < 0 {
		return nil, fmt.Errorf("%s names no digest", ref)
	}
	name, digest := ref[:at], ref[at+1:]
	if slash, colon := strings.LastIndex(name, "/"), strings.LastIndex(name, ":"); colon > slash {
		name = name[:colon]
	}
	host, repo := locate(name)
	if repo == "" {
		return nil, fmt.Errorf("%s is not a reference this lookup can read", ref)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	f := &fetcher{client: client, base: fmt.Sprintf("https://%s/v2/%s/", host, repo), repo: repo}

	type platform struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	}
	type manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests []struct {
			Digest   string   `json:"digest"`
			Platform platform `json:"platform"`
		} `json:"manifests"`
	}
	var top manifest
	if err := f.json("manifests/"+digest, accept, &top); err != nil {
		return nil, fmt.Errorf("reading %s: %w", ref, err)
	}
	type entry struct {
		platform string
		config   string
	}
	var entries []entry
	if len(top.Manifests) == 0 {
		entries = append(entries, entry{config: top.Config.Digest})
	}
	for _, m := range top.Manifests {
		if m.Platform.OS != "linux" {
			continue
		}
		var one manifest
		if err := f.json("manifests/"+m.Digest, accept, &one); err != nil {
			return nil, fmt.Errorf("reading %s: %w", ref, err)
		}
		p := m.Platform.Architecture
		if m.Platform.Variant != "" {
			p += "/" + m.Platform.Variant
		}
		entries = append(entries, entry{platform: p, config: one.Config.Digest})
	}
	var builds []Build
	for _, e := range entries {
		if e.config == "" {
			continue
		}
		var config struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			Variant      string `json:"variant"`
			Config       struct {
				Labels  map[string]string   `json:"Labels"`
				Volumes map[string]struct{} `json:"Volumes"`
			} `json:"config"`
		}
		if err := f.json("blobs/"+e.config, "application/json, */*", &config); err != nil {
			return nil, fmt.Errorf("reading %s's configuration: %w", ref, err)
		}
		b := Build{Platform: e.platform, Labels: config.Config.Labels}
		if b.Platform == "" {
			b.Platform = config.Architecture
			if config.Variant != "" {
				b.Platform += "/" + config.Variant
			}
			if config.OS != "" && config.OS != "linux" {
				b.Platform = config.OS + "/" + b.Platform
			}
		}
		for path := range config.Config.Volumes {
			b.Volumes = append(b.Volumes, path)
		}
		sort.Strings(b.Volumes)
		builds = append(builds, b)
	}
	return builds, nil
}

// fetcher reads one repository's documents, following the registry's
// anonymous challenge once and keeping the token it hands out.
type fetcher struct {
	client *http.Client
	base   string
	repo   string
	token  string
}

func (f *fetcher) json(path, accepts string, into any) error {
	resp, err := f.get(f.base+path, accepts)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && f.token == "" {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		if f.token, err = anonymousToken(f.client, challenge, f.repo); err != nil {
			return err
		}
		if resp, err = f.get(f.base+path, accepts); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the registry answered %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into)
}

func (f *fetcher) get(target, accepts string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accepts)
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	return f.client.Do(req)
}
