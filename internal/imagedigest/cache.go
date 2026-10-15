package imagedigest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Resolver says which build a reference names now, as Registry does.
type Resolver interface {
	Digest(ref string) (string, error)
}

// Cache answers each reference with the first digest r resolved it to, for
// as long as the Cache is kept: `paisans apply` keeps one for the whole run.
//
// A tag that moves can move during a run. `paisans apply` without --site
// plans each site more than once (the pull stage, then each site's apply, and
// pass two), and every one of those plans has to name the same build: the
// pull stage pulls the build it resolved, and an apply that resolved the tag
// again a minute later could name a newer one, which the host does not have,
// so it would pull it in the middle of the install and the earlier pull would
// have been for nothing. One answer per run keeps every plan on the build the
// first one showed.
//
// A failure is kept as well, for the same reason: every plan of the run sees
// the same answer, and a registry that took DefaultTimeout to fail once is not
// waited on again for every image and every plan. A failure that never
// reached the registry (a timeout, a refused connection, a name that does not
// resolve), or that the registry refused for rate limiting this address, is
// kept for its whole host, so every other image on it fails at once: each
// further request would be refused too, and would count against the same
// limit. A registry that answered, Eg: that a tag does not exist, fails that
// reference alone. The next run asks again. Calls for a reference that is
// being resolved wait for that resolution rather than start their own, so
// sites planned at the same time ask the registry once.
type Cache struct {
	r       Resolver
	mu      sync.Mutex
	digests map[string]string
	asking  map[string]*resolution
	// failed are the references whose resolution failed, and down the
	// registry hosts that could not be reached, with why.
	failed map[string]error
	down   map[string]error
	// versions are the version labels Version read, by reference. A build
	// named by its digest never changes, so an answer is kept for the run
	// like a digest is.
	versions map[string]string
	// platforms are the platform lists Platforms read, kept the same way.
	platforms map[string][]string
	// newest are the answers NewestVersion got, by reference.
	newest map[string]string
	// tracks are the answers Track got, by track, kept the way digests are:
	// every plan of a run puts a track on the same tag.
	tracks map[string]string

	// Store, when set, is a directory where Platforms keeps the answer for a
	// reference pinned by digest across runs, one file each. A digest names
	// one manifest, so its platform list never changes, and every apply
	// asks it of every image: kept, a run that moves nothing asks the
	// registry for no manifest, which is what Docker Hub counts against an
	// address's anonymous allowance.
	Store string
}

// Platformer names the platforms of a build, as Registry.Platforms does.
type Platformer interface {
	Platforms(ref string) ([]string, error)
}

// Platforms is the wrapped resolver's Platforms for ref, asked once per run.
// A build named by its digest never changes, so the answer is kept like a
// digest is. A failure is returned and not kept, and an unreachable one marks
// the host down as Digest does. A nil list with no error means the answer is
// unknown: the wrapped resolver cannot say, or the host is down, and apply
// skips the check rather than wait out a timeout per image.
func (c *Cache) Platforms(ref string) ([]string, error) {
	p, ok := c.r.(Platformer)
	if !ok {
		return nil, nil
	}
	c.mu.Lock()
	if got, ok := c.platforms[ref]; ok {
		c.mu.Unlock()
		return got, nil
	}
	if _, down := c.down[Host(ref)]; down {
		c.mu.Unlock()
		return nil, nil
	}
	c.mu.Unlock()
	if got, ok := c.stored(ref); ok {
		c.mu.Lock()
		c.platforms[ref] = got
		c.mu.Unlock()
		return got, nil
	}
	got, err := p.Platforms(ref)
	if err != nil {
		if Unreachable(err) {
			c.mu.Lock()
			c.down[Host(ref)] = err
			c.mu.Unlock()
		}
		return nil, err
	}
	c.mu.Lock()
	c.platforms[ref] = got
	c.mu.Unlock()
	c.store(ref, got)
	return got, nil
}

// storePath is where Store keeps ref's platforms, empty for a reference not
// pinned by digest, whose answer can change.
func (c *Cache) storePath(ref string) string {
	if c.Store == "" || !strings.Contains(ref, "@sha256:") {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(ref)))
	return filepath.Join(c.Store, hex.EncodeToString(sum[:])+".json")
}

func (c *Cache) stored(ref string) ([]string, bool) {
	path := c.storePath(ref)
	if path == "" {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var kept struct {
		Ref       string   `json:"ref"`
		Platforms []string `json:"platforms"`
	}
	if json.Unmarshal(raw, &kept) != nil || kept.Ref != strings.TrimSpace(ref) || len(kept.Platforms) == 0 {
		return nil, false
	}
	return kept.Platforms, true
}

// store keeps ref's platforms, best effort: one not kept is asked again.
func (c *Cache) store(ref string, platforms []string) {
	path := c.storePath(ref)
	if path == "" || len(platforms) == 0 {
		return
	}
	raw, err := json.Marshal(struct {
		Ref       string   `json:"ref"`
		Platforms []string `json:"platforms"`
	}{strings.TrimSpace(ref), platforms})
	if err != nil || os.MkdirAll(c.Store, 0o755) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// Versioner reads the version label of a build named by its digest, as
// Registry.Version does.
type Versioner interface {
	Version(ref string) (string, error)
}

// resolution is one call to the wrapped resolver, which later calls for the
// same reference wait on.
type resolution struct {
	done   chan struct{}
	digest string
	err    error
}

// Cached is a Cache in front of r.
func Cached(r Resolver) *Cache {
	return &Cache{r: r, digests: map[string]string{}, asking: map[string]*resolution{}, versions: map[string]string{}, platforms: map[string][]string{}, newest: map[string]string{}, tracks: map[string]string{}, failed: map[string]error{}, down: map[string]error{}}
}

// Digest is the digest ref was first resolved to, resolving it now if it has
// not been yet.
func (c *Cache) Digest(ref string) (string, error) {
	c.mu.Lock()
	if digest, ok := c.digests[ref]; ok {
		c.mu.Unlock()
		return digest, nil
	}
	if err, ok := c.failed[ref]; ok {
		c.mu.Unlock()
		return "", err
	}
	host := Host(ref)
	if err, ok := c.down[host]; ok {
		c.mu.Unlock()
		return "", fmt.Errorf("%s was not asked about %s, since it could not be reached earlier in this run: %w", host, ref, err)
	}
	if res, ok := c.asking[ref]; ok {
		c.mu.Unlock()
		<-res.done
		return res.digest, res.err
	}
	res := &resolution{done: make(chan struct{})}
	c.asking[ref] = res
	c.mu.Unlock()

	res.digest, res.err = c.r.Digest(ref)

	c.mu.Lock()
	delete(c.asking, ref)
	if res.err == nil {
		c.digests[ref] = res.digest
	} else {
		c.failed[ref] = res.err
		if Unreachable(res.err) {
			c.down[host] = res.err
		}
	}
	c.mu.Unlock()
	close(res.done)
	return res.digest, res.err
}

// Version is the wrapped resolver's Version for ref, asked once per run, and
// empty with no error when the wrapped resolver reads no labels. A failed
// read is asked again the next time, as a failed resolution is.
func (c *Cache) Version(ref string) (string, error) {
	v, ok := c.r.(Versioner)
	if !ok {
		return "", nil
	}
	c.mu.Lock()
	if version, ok := c.versions[ref]; ok {
		c.mu.Unlock()
		return version, nil
	}
	if _, down := c.down[Host(ref)]; down {
		c.mu.Unlock()
		return "", nil
	}
	c.mu.Unlock()
	version, err := v.Version(ref)
	if err != nil {
		// A label is for the operator's eye: a build whose label could not
		// be read shows its digest alone for the rest of the run, rather
		// than costing a timeout on every plan.
		version = ""
	}
	c.mu.Lock()
	c.versions[ref] = version
	c.mu.Unlock()
	return version, nil
}

// Lister names the newest version-shaped tag of a reference's repository,
// as Registry.NewestVersion does.
type Lister interface {
	NewestVersion(ref string) (string, error)
}

// NewestVersion is the wrapped resolver's NewestVersion for ref, asked once
// per run, and empty with no error when the wrapped resolver lists no tags
// or the registry could not be reached earlier in the run. It only fills in
// an example in advice, so a failure is an empty answer, not an error.
func (c *Cache) NewestVersion(ref string) (string, error) {
	l, ok := c.r.(Lister)
	if !ok {
		return "", nil
	}
	c.mu.Lock()
	if newest, ok := c.newest[ref]; ok {
		c.mu.Unlock()
		return newest, nil
	}
	if _, down := c.down[Host(ref)]; down {
		c.mu.Unlock()
		return "", nil
	}
	c.mu.Unlock()
	newest, err := l.NewestVersion(ref)
	if err != nil {
		newest = ""
	}
	c.mu.Lock()
	c.newest[ref] = newest
	c.mu.Unlock()
	return newest, nil
}

// Tracker names the newest tag on a track, as Registry.Track does.
type Tracker interface {
	Track(ref string) (string, error)
}

// Track is the wrapped resolver's Track for ref, asked once per run, so that
// every plan of the run, and every site in it, puts a track on the same tag.
// A failure is kept for the run, and one that never reached the registry
// marks its host down, as Digest does.
func (c *Cache) Track(ref string) (string, error) {
	t, ok := c.r.(Tracker)
	if !ok {
		return "", fmt.Errorf("resolving the track %s: this resolver lists no tags", ref)
	}
	c.mu.Lock()
	if got, ok := c.tracks[ref]; ok {
		c.mu.Unlock()
		return got, nil
	}
	if err, ok := c.failed["track "+ref]; ok {
		c.mu.Unlock()
		return "", err
	}
	host := Host(ref)
	if err, ok := c.down[host]; ok {
		c.mu.Unlock()
		return "", fmt.Errorf("%s was not asked about %s, since it could not be reached earlier in this run: %w", host, ref, err)
	}
	c.mu.Unlock()
	got, err := t.Track(ref)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.failed["track "+ref] = err
		if Unreachable(err) {
			c.down[host] = err
		}
		return "", err
	}
	if first, ok := c.tracks[ref]; ok {
		// Another plan asked at the same time and was answered first.
		return first, nil
	}
	c.tracks[ref] = got
	return got, nil
}

// Host is the registry host a reference is pulled from, by Docker's
// defaults (see split), empty for a reference this package cannot read.
func Host(ref string) string {
	name := ref
	if at := strings.Index(name, "@"); at >= 0 {
		name = name[:at]
	}
	if slash, colon := strings.LastIndex(name, "/"), strings.LastIndex(name, ":"); colon > slash {
		name = name[:colon]
	}
	host, _ := locate(name)
	return host
}
