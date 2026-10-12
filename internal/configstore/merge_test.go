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
			if !f.IsExported() || tag == "-" || tag == "" {
				continue
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
	default:
		panic(fmt.Sprintf("populate: %s is a %s, which the exhaustiveness test does not know", strings.Join(prefix, "."), v.Kind()))
	}
}

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
		if !header[p] {
			expected = append(expected, p)
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
	// And every path it produces is one a merge can write back.
	m, conflicts := mustMerge(t, &config.Secrets{}, &s, &config.Secrets{})
	if len(conflicts) != 0 || !reflect.DeepEqual(flatten(m), flatten(&s)) {
		t.Fatalf("a merge dropped paths: %v vs %v (%v)", flatten(m), flatten(&s), conflicts)
	}
}
