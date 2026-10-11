package imagedigest

import "sync"

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
// Only an answer is kept. A resolution that failed is asked again the next
// time, since a registry that did not answer once may answer now. Calls for a
// reference that is being resolved wait for that resolution rather than start
// their own, so sites planned at the same time ask the registry once.
type Cache struct {
	r       Resolver
	mu      sync.Mutex
	digests map[string]string
	asking  map[string]*resolution
	// versions are the version labels Version read, by reference. A build
	// named by its digest never changes, so an answer is kept for the run
	// like a digest is.
	versions map[string]string
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
	return &Cache{r: r, digests: map[string]string{}, asking: map[string]*resolution{}, versions: map[string]string{}}
}

// Digest is the digest ref was first resolved to, resolving it now if it has
// not been yet.
func (c *Cache) Digest(ref string) (string, error) {
	c.mu.Lock()
	if digest, ok := c.digests[ref]; ok {
		c.mu.Unlock()
		return digest, nil
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
	c.mu.Unlock()
	version, err := v.Version(ref)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.versions[ref] = version
	c.mu.Unlock()
	return version, nil
}
