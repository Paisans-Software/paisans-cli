package configstore

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/paisans-software/paisans-cli/internal/config"
	"github.com/paisans-software/paisans-cli/internal/progname"
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
// on the other is a conflict, and merged keeps ours' value there, or ours'
// removal. The merge is theirs with ours' changes applied, so the file's
// version and id are theirs.
//
// conflicts holds paths only, sorted. A value is never put in one, so a
// conflict is safe to print.
//
// The merge refuses while either side has a `paisans mesh rekey` or a
// `paisans secrets rotate` under way (underway).
func MergeSecrets(base, ours, theirs *config.Secrets) (merged *config.Secrets, conflicts []string, err error) {
	if err := underway(ours, theirs); err != nil {
		return nil, nil, err
	}
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
			// A conflict keeps ours.
			conflicts = append(conflicts, p)
			take = append(take, p)
		}
	}
	sort.Strings(conflicts)
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
	return merged, conflicts, nil
}

// underway refuses when either side holds the state of a run that a merge
// cannot combine: a mesh rekey (rekeyUnderway) or a secrets rotation
// (rotationUnderway).
func underway(ours, theirs *config.Secrets) error {
	if err := rekeyUnderway(ours, theirs); err != nil {
		return err
	}
	return rotationUnderway(ours, theirs)
}

// rekeyUnderway refuses when either side is part way through a `paisans mesh
// rekey`: a site with a next WireGuard key, or sites a `--all` run has left.
// A rekey's state is a sequence over live hosts, each step recorded as the
// host takes it, so combining two such states key by key could promote a key
// no host runs. A rekey finishes in minutes, so refusing costs one command on
// the side that holds it.
//
// Any next key counts, with or without mesh_rekey_remaining beside it: only a
// rekey writes one. The error names sites, never a key.
func rekeyUnderway(ours, theirs *config.Secrets) error {
	for _, side := range []struct {
		name, finish string
		s            *config.Secrets
	}{
		{"the local secrets", "run " + progname.Cmd() + " mesh rekey here to finish it, then pull", ours},
		{"the sites' newest version of the secrets", "finish it with " + progname.Cmd() + " mesh rekey on the workstation that started it and publish, then pull", theirs},
	} {
		sites := side.s.RekeyPending()
		for _, name := range side.s.MeshRekeyRemaining {
			if !slices.Contains(sites, name) {
				sites = append(sites, name)
			}
		}
		if len(sites) > 0 {
			return fmt.Errorf("%s have a mesh rekey under way (%s), which a merge cannot combine; %s", side.name, strings.Join(sites, ", "), side.finish)
		}
	}
	return nil
}

// rotationUnderway refuses when either side is part way through a `paisans
// secrets rotate`: a secret with a next value under rotations, or paths a
// `--all` run has left under rotate_remaining. A rotation's state is a
// sequence over live hosts, the checking side moved to the next value before
// its users are, so combining two such states key by key could promote a
// value no host's checking side accepts. Finishing the rotation on the side
// that holds it settles the state, so that is what the refusal names.
//
// The error names paths, never a value.
func rotationUnderway(ours, theirs *config.Secrets) error {
	for _, side := range []struct {
		name, finish string
		s            *config.Secrets
	}{
		{"the local secrets", "run " + progname.Cmd() + " secrets rotate here to finish it (with --all when the run was --all), then pull", ours},
		{"the sites' newest version of the secrets", "finish it with " + progname.Cmd() + " secrets rotate on the workstation that started it and publish, then pull", theirs},
	} {
		paths := side.s.RotationPending()
		for _, p := range side.s.RotateRemaining {
			if !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
		if len(paths) > 0 {
			return fmt.Errorf("%s have a secrets rotation under way (%s), which a merge cannot combine; %s", side.name, strings.Join(paths, ", "), side.finish)
		}
	}
	return nil
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
// it; the test beside this file fails until it is. mesh_rekey_remaining,
// rotations and rotate_remaining are not here because a merge refuses while
// any is set (underway), so there is never a value of them to combine.
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
	put("backup.restic_password", s.Backup.ResticPassword)
	put("backup.walg_libsodium_key", s.Backup.WALGLibsodiumKey)
	for name, d := range s.Backup.Destinations {
		put("backup.destinations."+name+".access_key_id", d.AccessKeyID)
		put("backup.destinations."+name+".secret_access_key", d.SecretAccessKey)
	}
	for name, site := range s.Sites {
		put("sites."+name+".wireguard_private_key", site.WireGuardPrivateKey)
		put("sites."+name+".wireguard_next_private_key", site.WireGuardNextPrivateKey)
		put("sites."+name+".heartbeat_token", site.HeartbeatToken)
		put("sites."+name+".beszel_token", site.BeszelToken)
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
	for name, a := range s.Operators {
		put("operators."+name+".dns_provider_email", a.DNSProviderEmail)
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
	c.Backup.Destinations = make(map[string]config.DestinationSecrets, len(s.Backup.Destinations))
	for k, v := range s.Backup.Destinations {
		c.Backup.Destinations[k] = v
	}
	c.PocketIDGroups = make(map[string]string, len(s.PocketIDGroups))
	for k, v := range s.PocketIDGroups {
		c.PocketIDGroups[k] = v
	}
	c.Operators = make(map[string]config.OperatorSecrets, len(s.Operators))
	for k, v := range s.Operators {
		c.Operators[k] = v
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
		site, from := dst.Sites[parts[1]], src.Sites[parts[1]]
		switch parts[2] {
		case "heartbeat_token":
			site.HeartbeatToken = from.HeartbeatToken
		case "beszel_token":
			site.BeszelToken = from.BeszelToken
		case "wireguard_next_private_key":
			site.WireGuardNextPrivateKey = from.WireGuardNextPrivateKey
		default:
			site.WireGuardPrivateKey = from.WireGuardPrivateKey
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
	case parts[0] == "backup" && len(parts) == 4:
		d, from := dst.Backup.Destinations[parts[2]], src.Backup.Destinations[parts[2]]
		if parts[3] == "access_key_id" {
			d.AccessKeyID = from.AccessKeyID
		} else {
			d.SecretAccessKey = from.SecretAccessKey
		}
		dst.Backup.Destinations[parts[2]] = d
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
		switch parts[2] {
		case "heartbeat_token":
			site.HeartbeatToken = ""
		case "beszel_token":
			site.BeszelToken = ""
		case "wireguard_next_private_key":
			site.WireGuardNextPrivateKey = ""
		default:
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
	case "backup":
		if len(parts) == 2 {
			return s.Set(path, "")
		}
		d := s.Backup.Destinations[parts[2]]
		if parts[3] == "access_key_id" {
			d.AccessKeyID = ""
		} else {
			d.SecretAccessKey = ""
		}
		s.Backup.Destinations[parts[2]] = d
		if d == (config.DestinationSecrets{}) {
			delete(s.Backup.Destinations, parts[2])
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
	want := map[string]int{"cluster": 2, "storage": 3, "sites": 3, "apps": 3, "external": 2, "oidc_clients": 3, "operators": 3}
	if parts[0] == "pocket_id_groups" {
		return len(parts) >= 2
	}
	if parts[0] == "backup" {
		return len(parts) == 2 || len(parts) == 4 && parts[1] == "destinations"
	}
	n, ok := want[parts[0]]
	return ok && len(parts) == n
}

func unroutable(path string) error {
	return fmt.Errorf("%s holds a name with a dot in it, which a merge cannot place", path)
}
