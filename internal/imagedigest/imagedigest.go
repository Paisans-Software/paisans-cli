// Package imagedigest asks a container registry which build a tag names now.
//
// The gateway's Caddy follows a tag that moves (acme.Published), and a host
// must still run a build that was shown to the operator and recorded. apply
// therefore turns the tag into the digest it names while it plans, and this
// package is how.
//
// It asks the registry itself, over the distribution API's HTTP interface,
// from the workstation running `paisans`. The answer is the registry's own
// Docker-Content-Digest for the tag's manifest, and for an image published for
// several platforms that is the digest of its index, which is exactly what
// `docker pull` records on the host as the image's RepoDigest: so the digest
// rendered here and the one a host reports are the same string. It needs
// nothing installed on the host, and a dry run that resolves changes nothing
// anywhere. Only anonymous pulls are spoken: a public image needs no more, and
// a private one fails here like an unreachable registry, which apply answers
// by keeping the build the host already runs.
package imagedigest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is a registry answering that the tag does not exist (404, the
// distribution API's MANIFEST_UNKNOWN), and ErrDenied one refusing the
// anonymous pull this lookup makes (401 or 403, from the manifest or from the
// token service). A failure that is neither did not get an answer about the
// tag: the registry was not reached, timed out, or failed on its side. apply
// gives each its own advice, so the registry's errors wrap these.
var (
	ErrNotFound = errors.New("not found")
	ErrDenied   = errors.New("access denied")
)

// answer is a registry's answer that was not a digest, with which of
// ErrNotFound and ErrDenied it was. Its text is the registry's answer alone.
type answer struct {
	text string
	kind error
}

func (a *answer) Error() string { return a.text }
func (a *answer) Unwrap() error { return a.kind }

// answered is the error for a registry that answered status, worded text,
// wrapping ErrNotFound or ErrDenied when status is one of theirs.
func answered(status int, text string) error {
	switch status {
	case http.StatusNotFound:
		return &answer{text, ErrNotFound}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &answer{text, ErrDenied}
	}
	return errors.New(text)
}

// Registry resolves a reference by asking its registry. The zero value is
// usable, with a client that gives up after Timeout.
type Registry struct {
	// Client makes the requests. Nil uses one with DefaultTimeout.
	Client *http.Client
}

// DefaultTimeout bounds one resolution. apply plans before it changes
// anything, and a registry that does not answer in this long is treated as
// unreachable rather than waited on.
const DefaultTimeout = 20 * time.Second

// accept is every manifest type a tag can name. The index types come first
// because a multi platform image is published as one, and its digest is the
// one a host records; a registry answers with whichever it stores.
var accept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Digest returns the digest ref's tag names now, as `sha256:<hex>`.
func (r Registry) Digest(ref string) (string, error) {
	host, repo, tag, err := split(ref)
	if err != nil {
		return "", err
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	manifest := fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, url.PathEscape(tag))

	token := ""
	resp, err := ask(client, http.MethodHead, manifest, token)
	if err != nil {
		return "", fmt.Errorf("asking %s for %s: %w", host, ref, err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		if token, err = anonymousToken(client, resp.Header.Get("WWW-Authenticate"), repo); err != nil {
			return "", fmt.Errorf("asking %s for %s: %w", host, ref, err)
		}
		if resp, err = ask(client, http.MethodHead, manifest, token); err != nil {
			return "", fmt.Errorf("asking %s for %s: %w", host, ref, err)
		}
		resp.Body.Close()
	}
	if digest := contentDigest(resp); resp.StatusCode == http.StatusOK && digest != "" {
		return digest, nil
	}
	return getDigest(client, manifest, token, ref, host, resp.StatusCode)
}

// getDigest is the fallback for a registry that answers HEAD without the
// digest header, or refuses HEAD: the manifest's own bytes hash to its
// digest, which is how the distribution API defines one.
func getDigest(client *http.Client, manifest, token, ref, host string, headStatus int) (string, error) {
	if headStatus == http.StatusNotFound {
		return "", answered(headStatus, fmt.Sprintf("%s answered that %s does not exist", host, ref))
	}
	resp, err := ask(client, http.MethodGet, manifest, token)
	if err != nil {
		return "", fmt.Errorf("asking %s for %s: %w", host, ref, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", answered(resp.StatusCode, fmt.Sprintf("%s answered %s for %s", host, resp.Status, ref))
	}
	if digest := contentDigest(resp); digest != "" {
		return digest, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("reading %s's manifest for %s: %w", host, ref, err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ask(client *http.Client, method, target, token string) (*http.Response, error) {
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

// contentDigest is the response's Docker-Content-Digest, when it is a sha256
// digest of the expected shape, and empty otherwise. Anything else is not
// something a compose file may be handed.
func contentDigest(resp *http.Response) string {
	d := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
	hexPart, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(hexPart) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return ""
	}
	return d
}

// anonymousToken follows a registry's Bearer challenge for a pull token,
// asking with no credentials, which is what a public image needs.
func anonymousToken(client *http.Client, challenge, repo string) (string, error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		// A registry that wants credentials before it says anything refuses
		// the anonymous pull as surely as a 401 after a token does.
		return "", &answer{fmt.Sprintf("the registry wants %q authentication, and only an anonymous bearer token is asked for", scheme), ErrDenied}
	}
	fields := challengeFields(params)
	realm := fields["realm"]
	if realm == "" {
		return "", fmt.Errorf("the registry's challenge names no realm to ask for a token")
	}
	u, err := url.Parse(realm)
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("the registry's token realm %q is not an https address", realm)
	}
	q := u.Query()
	if service := fields["service"]; service != "" {
		q.Set("service", service)
	}
	scope := fields["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return "", fmt.Errorf("asking for a pull token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", answered(resp.StatusCode, fmt.Sprintf("the token service answered %s; a private image needs credentials this lookup does not use", resp.Status))
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("reading the pull token: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", fmt.Errorf("the token service answered with no token")
}

// challengeFields reads `key="value",key="value"` from a WWW-Authenticate
// header. A comma inside quotes belongs to the value, as a scope with several
// actions has one.
func challengeFields(params string) map[string]string {
	out := map[string]string{}
	for params != "" {
		params = strings.TrimLeft(params, " ,")
		key, rest, ok := strings.Cut(params, "=")
		if !ok {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))
		var value string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				value, rest = rest[1:], ""
			} else {
				value, rest = rest[1:1+end], rest[2+end:]
			}
		} else {
			value, rest, _ = strings.Cut(rest, ",")
		}
		out[key] = value
		params = rest
	}
	return out
}

// split turns a reference with a tag into the registry host, the repository
// and the tag, applying Docker's own defaults: a first path component is a
// registry only when it holds a dot or a colon or is localhost, everything
// else is on Docker Hub, and a one component name there is under library/.
func split(ref string) (host, repo, tag string, err error) {
	if strings.Contains(ref, "@") {
		return "", "", "", fmt.Errorf("%s already names a digest", ref)
	}
	name := ref
	slash := strings.LastIndex(name, "/")
	colon := strings.LastIndex(name, ":")
	if colon <= slash {
		return "", "", "", fmt.Errorf("%s names no tag to resolve", ref)
	}
	name, tag = name[:colon], name[colon+1:]
	host, repo = locate(name)
	if repo == "" || tag == "" {
		return "", "", "", fmt.Errorf("%s is not a reference this lookup can read", ref)
	}
	return host, repo, tag, nil
}

// locate is the registry host and the repository a name without a tag or a
// digest is on, by Docker's defaults, as split describes.
func locate(name string) (host, repo string) {
	first, rest, found := strings.Cut(name, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host, repo = first, rest
	} else {
		host, repo = "docker.io", name
	}
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
	}
	return host, repo
}
