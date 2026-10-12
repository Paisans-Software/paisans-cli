package configstore

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-stack/internal/config"
)

// MergeSecrets merges two edits of the secrets file made from one base: ours,
// the local file, and theirs, the sites' newest version. A text merge of the
// encrypted files cannot work, so the three are compared key by key, by the
// dotted paths Secrets.Get and Secrets.Set accept.
//
// A key changed on one side only takes that side's value; a key changed the
// same way on both takes it; a key changed differently on the two sides is a
// conflict. A removal counts as a change, so a key removed on one side and
// unchanged on the other is removed, and one removed on one side and changed
// on the other is a conflict. The merge is theirs with ours' changes applied,
// so the file's version and id are theirs.
//
// conflicts holds paths only, sorted. A value is never put in one, so a
// conflict is safe to print, and merged is nil when there is any.
func MergeSecrets(base, ours, theirs *config.Secrets) (merged *config.Secrets, conflicts []string, err error) {
	b, o, t := flatten(base), flatten(ours), flatten(theirs)
	paths := map[string]bool{}
	for _, m := range []map[string]string{b, o, t} {
		for p := range m {
			paths[p] = true
		}
	}
	var take []string
	for p := range paths {
		switch {
		case same(o, t, p):
		case same(o, b, p):
		case same(t, b, p):
			take = append(take, p)
		default:
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return nil, conflicts, nil
	}
	sort.Strings(take)
	merged = clone(theirs)
	for _, p := range take {
		if _, ok := o[p]; ok {
			err = assign(merged, ours, p)
		} else {
			err = remove(merged, p)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return merged, nil, nil
}

// same reports whether path holds the same value, or is absent, in both.
func same(a, b map[string]string, path string) bool {
	va, inA := a[path]
	vb, inB := b[path]
	return inA == inB && va == vb
}

// flatten is every secret the file holds by the dotted path Get and Set use.
// A fixed field that is empty is absent, as Get reports it. The file's
// version and id are not secrets and are not here.
//
// A field added to config.Secrets has to be added here, or a merge would drop
// it; the test beside this file fails until it is.
func flatten(s *config.Secrets) map[string]string {
	out := map[string]string{}
	put := func(path, v string) {
		if v != "" {
			out[path] = v
		}
	}
	put("cluster.superuser_password", s.Cluster.SuperuserPassword)
	put("cluster.admin_password", s.Cluster.AdminPassword)
	put("cluster.standby_password", s.Cluster.StandbyPassword)
	put("storage.garage.admin_token", s.Storage.Garage.AdminToken)
	put("storage.garage.rpc_secret", s.Storage.Garage.RPCSecret)
	for name, site := range s.Sites {
		put("sites."+name+".wireguard_private_key", site.WireGuardPrivateKey)
		put("sites."+name+".heartbeat_token", site.HeartbeatToken)
	}
	for app, values := range s.Apps {
		for key, v := range values {
			out["apps."+app+"."+key] = fmt.Sprint(v)
		}
	}
	for name, v := range s.External {
		out["external."+name] = v
	}
	for name, client := range s.OIDCClients {
		put("oidc_clients."+name+".client_id", client.ClientID)
		put("oidc_clients."+name+".client_secret", client.ClientSecret)
	}
	for name, v := range s.PocketIDGroups {
		out["pocket_id_groups."+name] = v
	}
	return out
}

// clone is a copy of s that shares no map with it.
func clone(s *config.Secrets) *config.Secrets {
	c := *s
	c.Sites = make(map[string]config.SiteSecrets, len(s.Sites))
	for k, v := range s.Sites {
		c.Sites[k] = v
	}
	c.Apps = make(map[string]map[string]any, len(s.Apps))
	for app, values := range s.Apps {
		c.Apps[app] = make(map[string]any, len(values))
		for k, v := range values {
			c.Apps[app][k] = v
		}
	}
	c.External = make(map[string]string, len(s.External))
	for k, v := range s.External {
		c.External[k] = v
	}
	c.OIDCClients = make(map[string]config.OIDCClient, len(s.OIDCClients))
	for k, v := range s.OIDCClients {
		c.OIDCClients[k] = v
	}
	c.PocketIDGroups = make(map[string]string, len(s.PocketIDGroups))
	for k, v := range s.PocketIDGroups {
		c.PocketIDGroups[k] = v
	}
	return &c
}

// assign sets path in dst to the value src holds there. It writes the maps
// directly: Set refuses names it would not let an operator create, and a
// name already in a file is not its to judge.
func assign(dst, src *config.Secrets, path string) error {
	parts := strings.Split(path, ".")
	if !routable(parts) {
		return unroutable(path)
	}
	switch {
	case parts[0] == "apps" && len(parts) == 3:
		if dst.Apps[parts[1]] == nil {
			dst.Apps[parts[1]] = map[string]any{}
		}
		dst.Apps[parts[1]][parts[2]] = src.Apps[parts[1]][parts[2]]
	case parts[0] == "external" && len(parts) == 2:
		dst.External[parts[1]] = src.External[parts[1]]
	case parts[0] == "pocket_id_groups":
		name := strings.Join(parts[1:], ".")
		dst.PocketIDGroups[name] = src.PocketIDGroups[name]
	case parts[0] == "sites" && len(parts) == 3:
		site := dst.Sites[parts[1]]
		if parts[2] == "heartbeat_token" {
			site.HeartbeatToken = src.Sites[parts[1]].HeartbeatToken
		} else {
			site.WireGuardPrivateKey = src.Sites[parts[1]].WireGuardPrivateKey
		}
		dst.Sites[parts[1]] = site
	case parts[0] == "oidc_clients" && len(parts) == 3:
		client := dst.OIDCClients[parts[1]]
		if parts[2] == "client_id" {
			client.ClientID = src.OIDCClients[parts[1]].ClientID
		} else {
			client.ClientSecret = src.OIDCClients[parts[1]].ClientSecret
		}
		dst.OIDCClients[parts[1]] = client
	default:
		v, _ := src.Get(path)
		return dst.Set(path, v)
	}
	return nil
}

// remove deletes path from s, and the map entry around it when that leaves
// nothing in it.
func remove(s *config.Secrets, path string) error {
	parts := strings.Split(path, ".")
	if !routable(parts) {
		return unroutable(path)
	}
	switch parts[0] {
	case "apps":
		delete(s.Apps[parts[1]], parts[2])
		if len(s.Apps[parts[1]]) == 0 {
			delete(s.Apps, parts[1])
		}
	case "external":
		delete(s.External, parts[1])
	case "pocket_id_groups":
		delete(s.PocketIDGroups, strings.Join(parts[1:], "."))
	case "sites":
		site := s.Sites[parts[1]]
		if parts[2] == "heartbeat_token" {
			site.HeartbeatToken = ""
		} else {
			site.WireGuardPrivateKey = ""
		}
		s.Sites[parts[1]] = site
		if site == (config.SiteSecrets{}) {
			delete(s.Sites, parts[1])
		}
	case "oidc_clients":
		client := s.OIDCClients[parts[1]]
		if parts[2] == "client_id" {
			client.ClientID = ""
		} else {
			client.ClientSecret = ""
		}
		s.OIDCClients[parts[1]] = client
		if client == (config.OIDCClient{}) {
			delete(s.OIDCClients, parts[1])
		}
	default:
		return s.Set(path, "")
	}
	return nil
}

// Flatten is every secret the file holds by dotted path, as MergeSecrets
// compares them.
func Flatten(s *config.Secrets) map[string]string { return flatten(s) }

// routable reports whether a path has the segments its section has. A name
// with a dot in it flattens to a path with one too many, which cannot be told
// from a deeper key, so it is refused rather than written somewhere else.
// Pocket ID group names are the one section whose name is the rest of the path.
func routable(parts []string) bool {
	want := map[string]int{"cluster": 2, "storage": 3, "sites": 3, "apps": 3, "external": 2, "oidc_clients": 3}
	if parts[0] == "pocket_id_groups" {
		return len(parts) >= 2
	}
	n, ok := want[parts[0]]
	return ok && len(parts) == n
}

func unroutable(path string) error {
	return fmt.Errorf("%s holds a name with a dot in it, which a merge cannot place", path)
}
