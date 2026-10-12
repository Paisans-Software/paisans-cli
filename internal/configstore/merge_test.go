package configstore

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/config"
)

func secretsWith(kv ...string) *config.Secrets {
	s := &config.Secrets{Version: config.SecretsVersion}
	for i := 0; i < len(kv); i += 2 {
		if err := s.Set(kv[i], kv[i+1]); err != nil {
			panic(err)
		}
	}
	return s
}

func mustMerge(t *testing.T, base, ours, theirs *config.Secrets) (*config.Secrets, []string) {
	t.Helper()
	m, conflicts, err := MergeSecrets(base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	return m, conflicts
}

func TestMergeDisjointEditsMerge(t *testing.T) {
	base := secretsWith("external.a", "1", "external.b", "1")
	ours := secretsWith("external.a", "2", "external.b", "1")
	theirs := secretsWith("external.a", "1", "external.b", "3", "apps.talk.key", "k")
	m, conflicts := mustMerge(t, base, ours, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("conflicts %v", conflicts)
	}
	want := map[string]string{"external.a": "2", "external.b": "3", "apps.talk.key": "k"}
	if got := flatten(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged %v, want %v", got, want)
	}
}

func TestMergeSameEditOnBothSidesMerges(t *testing.T) {
	base := secretsWith("external.a", "1")
	both := secretsWith("external.a", "2")
	m, conflicts := mustMerge(t, base, both, both)
	if v, _ := m.Get("external.a"); len(conflicts) != 0 || v != "2" {
		t.Fatalf("got %q, conflicts %v", v, conflicts)
	}
}

func TestMergeDifferentEditsOfOneKeyConflictWithoutValues(t *testing.T) {
	base := secretsWith("external.a", "base-value", "external.b", "1")
	ours := secretsWith("external.a", "ours-value", "external.b", "1")
	theirs := secretsWith("external.a", "theirs-value", "external.b", "2")
	m, conflicts, err := MergeSecrets(base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(conflicts, []string{"external.a"}) {
		t.Fatalf("conflicts %v", conflicts)
	}
	shown := fmt.Sprint(conflicts, err, m == nil)
	for _, v := range []string{"base-value", "ours-value", "theirs-value"} {
		if strings.Contains(shown, v) {
			t.Errorf("%q appears in %s", v, shown)
		}
	}
}

func TestMergeRemovals(t *testing.T) {
	base := secretsWith("external.a", "1", "external.b", "1", "external.c", "1")
	ours := secretsWith("external.b", "1", "external.c", "1")   // removes a
	theirs := secretsWith("external.a", "1", "external.b", "1") // removes c
	m, conflicts := mustMerge(t, base, ours, theirs)
	if got := flatten(m); len(conflicts) != 0 || !reflect.DeepEqual(got, map[string]string{"external.b": "1"}) {
		t.Fatalf("merged %v, conflicts %v", got, conflicts)
	}
	// Removed on one side, changed on the other, is a conflict.
	changed := secretsWith("external.a", "2", "external.b", "1", "external.c", "1")
	_, conflicts = mustMerge(t, base, ours, changed)
	if !reflect.DeepEqual(conflicts, []string{"external.a"}) {
		t.Fatalf("conflicts %v", conflicts)
	}
}

func TestMergeKeepsTheStructFieldsAndLeavesInputsAlone(t *testing.T) {
	base := secretsWith("sites.a.heartbeat_token", "t")
	ours := secretsWith("sites.a.heartbeat_token", "t", "pocket_id_groups.members", "g1")
	theirs := secretsWith("sites.a.heartbeat_token", "t", "oidc_clients.talk.client_id", "c")
	theirs.ID = "id-1"
	m, conflicts := mustMerge(t, base, ours, theirs)
	if len(conflicts) != 0 || m.ID != "id-1" || m.Version != config.SecretsVersion {
		t.Fatalf("%+v %v", m, conflicts)
	}
	if v, _ := m.Get("pocket_id_groups.members"); v != "g1" {
		t.Errorf("group %q", v)
	}
	if _, ok := theirs.Get("pocket_id_groups.members"); ok {
		t.Error("theirs was modified")
	}
}

// populated fills every yaml-tagged field of v, and returns the paths the
// flattener is expected to produce for it, using "k" for every map key.
func populated(v reflect.Value, prefix []string, paths *[]string) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if !f.IsExported() {
				continue
			}
			if tag == "-" {
				continue
			}
			if tag == "" || strings.Contains(f.Tag.Get("yaml"), "inline") {
				panic(fmt.Sprintf("field %s has no yaml name or is inlined; teach flatten and this test about it", f.Name))
			}
			populated(v.Field(i), append(append([]string{}, prefix...), tag), paths)
		}
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		elem := reflect.New(v.Type().Elem()).Elem()
		if elem.Kind() == reflect.Interface {
			elem = reflect.ValueOf("v")
		} else {
			populated(elem, append(append([]string{}, prefix...), "k"), paths)
			v.SetMapIndex(reflect.ValueOf("k"), elem)
			return
		}
		v.SetMapIndex(reflect.ValueOf("k"), elem)
		*paths = append(*paths, strings.Join(append(append([]string{}, prefix...), "k"), "."))
	case reflect.String:
		v.SetString("x")
		*paths = append(*paths, strings.Join(prefix, "."))
	case reflect.Int:
		v.SetInt(1)
		*paths = append(*paths, strings.Join(prefix, "."))
	case reflect.Slice:
		// A slice is never a secret the merge combines; a field that is one
		// must be named in refusedWhileSet, or the test fails on it.
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), reflect.New(v.Type().Elem()).Elem()))
		if v.Index(0).Kind() == reflect.String {
			v.Index(0).SetString("x")
		}
		*paths = append(*paths, strings.Join(prefix, "."))
	default:
		panic(fmt.Sprintf("populate: %s is a %s, which the exhaustiveness test does not know", strings.Join(prefix, "."), v.Kind()))
	}
}

// refusedWhileSet is every field the flattener leaves out because a merge
// refuses while it is set, so there is never a value of it to combine. A new
// field goes here only once someone has decided a merge refuses on it, and
// underway checks it; anything else the flattener misses fails the test. A
// map is named by its own field, and covers every key under it.
var refusedWhileSet = map[string]bool{"mesh_rekey_remaining": true, "rotations": true, "rotate_remaining": true}

// A field added to Secrets that the flattener does not cover would be
// dropped by every merge. This fails first. Version and ID are the file's
// header, taken whole from the head.
func TestFlattenCoversEverySecretsField(t *testing.T) {
	var s config.Secrets
	var want []string
	populated(reflect.ValueOf(&s).Elem(), nil, &want)
	var header = map[string]bool{"version": true, "id": true}
	var expected []string
	for _, p := range want {
		if !header[p] && !refusedWhileSet[strings.Split(p, ".")[0]] {
			expected = append(expected, p)
		}
	}
	for p := range refusedWhileSet {
		var only config.Secrets
		i := fieldByYAML(t, p)
		reflect.ValueOf(&only).Elem().FieldByIndex(i).Set(reflect.ValueOf(s).FieldByIndex(i))
		if underway(&only, &config.Secrets{}) == nil {
			t.Errorf("%s is listed as refused while set, and a merge does not refuse on it", p)
		}
		if underway(&config.Secrets{}, &only) == nil {
			t.Errorf("%s is listed as refused while set, and a merge does not refuse on theirs", p)
		}
	}
	var got []string
	for p := range flatten(&s) {
		got = append(got, p)
	}
	sort.Strings(expected)
	sort.Strings(got)
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("flatten produced\n%v\nthe struct has\n%v", got, expected)
	}
	// And every path it produces is one a merge can write back, and take out
	// again. assign and remove are what the merge calls; the merge itself
	// refuses this file, which has a rekey under way.
	m := clone(&config.Secrets{})
	for p := range flatten(&s) {
		if err := assign(m, &s, p); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(flatten(m), flatten(&s)) {
		t.Fatalf("assign dropped paths: %v vs %v", flatten(m), flatten(&s))
	}
	for p := range flatten(&s) {
		if err := remove(m, p); err != nil {
			t.Fatal(err)
		}
	}
	if left := flatten(m); len(left) != 0 {
		t.Fatalf("remove left %v", left)
	}
}

// fieldByYAML is the index of the Secrets field whose yaml name is name.
func fieldByYAML(t *testing.T, name string) []int {
	t.Helper()
	typ := reflect.TypeOf(config.Secrets{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.Split(typ.Field(i).Tag.Get("yaml"), ",")[0] == name {
			return []int{i}
		}
	}
	t.Fatalf("Secrets has no field %s", name)
	return nil
}

// Each site's next key is a site secret like its current one, and a merge
// keeps it apart from the current key.
func TestFlattenCarriesTheNextKeyApart(t *testing.T) {
	s := secretsWith("sites.a.wireguard_private_key", "current")
	site := s.Sites["a"]
	site.WireGuardNextPrivateKey = "next"
	s.Sites["a"] = site
	got := flatten(s)
	if got["sites.a.wireguard_private_key"] != "current" || got["sites.a.wireguard_next_private_key"] != "next" {
		t.Fatalf("flatten %v", got)
	}
}

// withNextKey is s with site's next WireGuard key set, as a `paisans mesh
// rekey` under way leaves it.
func withNextKey(s *config.Secrets, site string) *config.Secrets {
	v := s.Sites[site]
	v.WireGuardNextPrivateKey = "next-sentinel"
	s.Sites[site] = v
	return s
}

func TestMergeRefusesWhileOursHasARekeyUnderWay(t *testing.T) {
	base := secretsWith("sites.home-a.wireguard_private_key", "k", "external.x", "1")
	ours := withNextKey(secretsWith("sites.home-a.wireguard_private_key", "k", "external.x", "1"), "home-a")
	theirs := secretsWith("sites.home-a.wireguard_private_key", "k", "external.x", "2")
	m, conflicts, err := MergeSecrets(base, ours, theirs)
	if err == nil || m != nil || conflicts != nil {
		t.Fatalf("merged %v %v %v", m, conflicts, err)
	}
	for _, want := range []string{"local", "home-a", "paisans mesh rekey"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "next-sentinel") {
		t.Errorf("the refusal quotes the key: %v", err)
	}
}

func TestMergeRefusesWhileTheirsHasARekeyRemaining(t *testing.T) {
	base := secretsWith("external.x", "1")
	ours := secretsWith("external.x", "2")
	theirs := secretsWith("external.x", "1")
	theirs.MeshRekeyRemaining = []string{"home-b", "vm"}
	_, _, err := MergeSecrets(base, ours, theirs)
	if err == nil {
		t.Fatal("merged")
	}
	for _, want := range []string{"newest version", "home-b", "paisans mesh rekey"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "local") {
		t.Errorf("the refusal blames the local side: %v", err)
	}
}

// A next key is only ever written by a rekey, so one alone, with no
// mesh_rekey_remaining beside it, is a rekey under way all the same.
func TestMergeRefusesOnANextKeyAlone(t *testing.T) {
	base := secretsWith("external.x", "1")
	ours := secretsWith("external.x", "1")
	theirs := withNextKey(secretsWith("external.x", "1", "sites.vm.heartbeat_token", "t"), "vm")
	if _, _, err := MergeSecrets(base, ours, theirs); err == nil || !strings.Contains(err.Error(), "vm") {
		t.Fatalf("got %v", err)
	}
}

func TestMergeRefusesANameWithADot(t *testing.T) {
	base := secretsWith("external.x", "1")
	ours := secretsWith("external.x", "1")
	ours.External["a.b"] = "sentinel"
	theirs := secretsWith("external.x", "1")
	_, _, err := MergeSecrets(base, ours, theirs)
	if err == nil || !strings.Contains(err.Error(), "external.a.b") || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("got %v", err)
	}
}

func TestMergeRefusesWhileOursHasARotationPending(t *testing.T) {
	base := secretsWith("cluster.admin_password", "old", "external.x", "1")
	ours := secretsWith("cluster.admin_password", "old", "external.x", "1")
	ours.Rotations = map[string]string{"cluster.admin_password": "next-sentinel"}
	theirs := secretsWith("cluster.admin_password", "old", "external.x", "2")
	m, conflicts, err := MergeSecrets(base, ours, theirs)
	if err == nil || m != nil || conflicts != nil {
		t.Fatalf("merged %v %v %v", m, conflicts, err)
	}
	for _, want := range []string{"local", "cluster.admin_password", "paisans secrets rotate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	for _, not := range []string{"next-sentinel", "--restart"} {
		if strings.Contains(err.Error(), not) {
			t.Errorf("the refusal says %q: %v", not, err)
		}
	}
}

// A stopped --all run between two secrets has no rotation pending and a list
// left; the list is part of the run's state all the same.
func TestMergeRefusesWhileTheirsHasARotateRemaining(t *testing.T) {
	base := secretsWith("external.x", "1")
	ours := secretsWith("external.x", "2")
	theirs := secretsWith("external.x", "1")
	theirs.RotateRemaining = []string{"storage.garage.admin_token", "apps.talk.oauth"}
	_, _, err := MergeSecrets(base, ours, theirs)
	if err == nil {
		t.Fatal("merged")
	}
	for _, want := range []string{"newest version", "storage.garage.admin_token", "paisans secrets rotate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "local") || strings.Contains(err.Error(), "--restart") {
		t.Errorf("the refusal blames the local side or offers --restart: %v", err)
	}
}

// The backup keys and each destination's credentials merge like any other
// secret: a destination one side takes out leaves no empty entry, and one
// side's change to one field of a destination keeps the other field.
func TestMergeBackupSecrets(t *testing.T) {
	base := secretsWith("backup.restic_password", "r",
		"backup.destinations.old.access_key_id", "old-id", "backup.destinations.old.secret_access_key", "old-secret",
		"backup.destinations.b2.access_key_id", "b2-id", "backup.destinations.b2.secret_access_key", "b2-secret")
	// Ours takes old out, replaces b2's secret, and adds the WAL-G key.
	ours := secretsWith("backup.restic_password", "r", "backup.walg_libsodium_key", "w",
		"backup.destinations.b2.access_key_id", "b2-id", "backup.destinations.b2.secret_access_key", "b2-next")
	// Theirs adds a destination and leaves the rest alone.
	theirs := secretsWith("backup.restic_password", "r",
		"backup.destinations.old.access_key_id", "old-id", "backup.destinations.old.secret_access_key", "old-secret",
		"backup.destinations.b2.access_key_id", "b2-id", "backup.destinations.b2.secret_access_key", "b2-secret",
		"backup.destinations.new.access_key_id", "new-id", "backup.destinations.new.secret_access_key", "new-secret")
	m, conflicts := mustMerge(t, base, ours, theirs)
	if len(conflicts) != 0 {
		t.Fatalf("conflicts %v", conflicts)
	}
	want := map[string]string{
		"backup.restic_password": "r", "backup.walg_libsodium_key": "w",
		"backup.destinations.b2.access_key_id": "b2-id", "backup.destinations.b2.secret_access_key": "b2-next",
		"backup.destinations.new.access_key_id": "new-id", "backup.destinations.new.secret_access_key": "new-secret",
	}
	if got := flatten(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged %v, want %v", got, want)
	}
	if _, ok := m.Backup.Destinations["old"]; ok {
		t.Error("a destination removed on one side left an empty entry")
	}
	if theirs.Backup.Destinations["b2"].SecretAccessKey != "b2-secret" || len(theirs.Backup.Destinations) != 3 {
		t.Error("the merge wrote into its input")
	}
}
