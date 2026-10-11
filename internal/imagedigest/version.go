package imagedigest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// VersionLabel is the label an image's build records its version in. Both
// forks the toolkit runs set it through docker/metadata-action, and so do
// most images built that way.
const VersionLabel = "org.opencontainers.image.version"

// Version returns the version label of the build ref names, which must carry
// its digest (Eg: ghcr.io/paisans-software/mbin:stack-1@sha256:...), empty
// when the build has no such label. It reads the build's image configuration,
// anonymously as Digest does: for an image published for several platforms,
// that of linux/amd64, or of the first platform the index lists when it has
// none, since one release labels every platform alike.
//
// It is for the dry run, which names the versions a moved image goes between
// beside its digests. A digest is what pins a build; a label is what a
// publisher wrote in it, and a build without one is not an error.
func (r Registry) Version(ref string) (string, error) {
	at := strings.LastIndex(ref, "@")
	if at < 0 {
		return "", fmt.Errorf("%s names no digest", ref)
	}
	name, digest := ref[:at], ref[at+1:]
	if slash, colon := strings.LastIndex(name, "/"), strings.LastIndex(name, ":"); colon > slash {
		name = name[:colon]
	}
	host, repo := locate(name)
	if repo == "" {
		return "", fmt.Errorf("%s is not a reference this lookup can read", ref)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	f := &fetcher{client: client, base: fmt.Sprintf("https://%s/v2/%s/", host, repo), repo: repo}

	var manifest struct {
		MediaType string `json:"mediaType"`
		Config    struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := f.json("manifests/"+digest, accept, &manifest); err != nil {
		return "", fmt.Errorf("reading %s: %w", ref, err)
	}
	if len(manifest.Manifests) > 0 {
		pick := ""
		for _, m := range manifest.Manifests {
			if m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
				pick = m.Digest
				break
			}
			if pick == "" && m.Platform.OS != "unknown" {
				pick = m.Digest
			}
		}
		if pick == "" {
			return "", nil
		}
		manifest.Config.Digest = ""
		if err := f.json("manifests/"+pick, accept, &manifest); err != nil {
			return "", fmt.Errorf("reading %s: %w", ref, err)
		}
	}
	if manifest.Config.Digest == "" {
		return "", nil
	}
	var config struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := f.json("blobs/"+manifest.Config.Digest, "application/json, */*", &config); err != nil {
		return "", fmt.Errorf("reading %s's configuration: %w", ref, err)
	}
	return config.Config.Labels[VersionLabel], nil
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
