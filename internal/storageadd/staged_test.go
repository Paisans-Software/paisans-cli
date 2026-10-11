package storageadd_test

import (
	"reflect"
	"testing"

	"github.com/paisans-software/paisans-stack/internal/storageadd"
)

// current is `garage layout show`'s current layout as dxflrs/garage:v1.0.1
// prints it (src/garage/cli/layout.rs, cmd_show_layout and
// print_cluster_layout), columns padded by format_table, with the RPC
// client's log line before it.
const current = "Connection established to 51494feb5444d466\n" +
	"==== CURRENT CLUSTER LAYOUT ====\n" +
	"ID                Tags  Zone    Capacity  Usable capacity\n" +
	"51494feb5444d466        home-a  100.0 GB  100.0 GB (100.0%)\n" +
	"8c2a5b1e0f3d4c7a        home-b  100.0 GB  100.0 GB (100.0%)\n" +
	"e3f1d2c4b5a69788        home-c  100.0 GB  100.0 GB (100.0%)\n" +
	"\n" +
	"Zone redundancy: maximum\n" +
	"\n" +
	"Current cluster layout version: 3\n"

// after is what follows a staged section the new layout computes from
// (print_staging_role_changes, then cmd_show_layout).
const after = "\n" +
	"==== NEW CLUSTER LAYOUT AFTER APPLYING CHANGES ====\n" +
	"ID                Tags  Zone    Capacity  Usable capacity\n" +
	"51494feb5444d466        home-a  100.0 GB  100.0 GB (100.0%)\n" +
	"e3f1d2c4b5a69788        home-c  100.0 GB  100.0 GB (100.0%)\n" +
	"\n" +
	"Zone redundancy: maximum\n" +
	"\n" +
	"Optimal partition size:                     390.6 MB\n" +
	"Usable capacity / total cluster capacity:   200.0 GB / 200.0 GB (100.0 %)\n" +
	"Effective capacity (replication factor 2):  100.0 GB\n" +
	"\n" +
	"To enact the staged role changes, type:\n" +
	"\n" +
	"    garage layout apply --version 4\n" +
	"\n" +
	"You can also revert all proposed changes with: garage layout revert\n"

func TestLayoutStaged(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want storageadd.LayoutStaging
		any  bool
	}{
		// With nothing staged Garage prints no staged section at all, not an
		// empty one.
		{name: "nothing staged", out: current},
		{name: "no roles at all", out: "==== CURRENT CLUSTER LAYOUT ====\nNo nodes currently have a role in the cluster.\nSee `garage status` to view available nodes.\n\nCurrent cluster layout version: 0\n"},
		{name: "a removal", any: true,
			out: current + "\n==== STAGED ROLE CHANGES ====\n" +
				"ID                Tags  Zone  Capacity\n" +
				"8c2a5b1e0f3d4c7a  REMOVED\n\n" + after,
			want: storageadd.LayoutStaging{Removed: []string{"8c2a5b1e0f3d4c7a"}}},
		{name: "an assignment with no tags", any: true,
			out: current + "\n==== STAGED ROLE CHANGES ====\n" +
				"ID                Tags  Zone    Capacity\n" +
				"0a1b2c3d4e5f6071        home-d  100.0 GB\n\n" + after,
			want: storageadd.LayoutStaging{Assigned: []string{"0a1b2c3d4e5f6071"}}},
		{name: "a removal and a resize with tags", any: true,
			out: current + "\n==== STAGED ROLE CHANGES ====\n" +
				"ID                Tags     Zone    Capacity\n" +
				"e3f1d2c4b5a69788  ssd,big  home-c  200.0 GB\n" +
				"8c2a5b1e0f3d4c7a  REMOVED\n\n" + after,
			want: storageadd.LayoutStaging{Removed: []string{"8c2a5b1e0f3d4c7a"}, Assigned: []string{"e3f1d2c4b5a69788"}}},
		{name: "only a zone redundancy", any: true,
			out:  current + "\n==== STAGED ROLE CHANGES ====\nZone redundancy: 2\n" + after,
			want: storageadd.LayoutStaging{Parameters: true}},
		{name: "a change the new layout cannot be computed from", any: true,
			out: current + "\n==== STAGED ROLE CHANGES ====\n" +
				"ID                Tags  Zone  Capacity\n" +
				"51494feb5444d466  REMOVED\n" +
				"8c2a5b1e0f3d4c7a  REMOVED\n\n" +
				"Error while trying to compute the assignment: The number of nodes with positive capacity (1) is smaller than the replication factor (2).\n" +
				"This new layout cannot yet be applied.\n" +
				"You can also revert all proposed changes with: garage layout revert\n",
			want: storageadd.LayoutStaging{Removed: []string{"51494feb5444d466", "8c2a5b1e0f3d4c7a"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := storageadd.LayoutStaged(tc.out)
			if got.Any() != tc.any {
				t.Errorf("Any() = %v, want %v", got.Any(), tc.any)
			}
			got.Seen = false
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("LayoutStaged = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// A staged section whose rows this parser cannot read still counts as
// staged: what cannot be read cannot be shown to be harmless.
func TestAnUnreadStagedSectionIsStaged(t *testing.T) {
	got := storageadd.LayoutStaged(current + "\n==== STAGED ROLE CHANGES ====\nsomething new\n")
	if !got.Any() || got.Only("8c2a5b1e0f3d4c7a") {
		t.Errorf("LayoutStaged = %#v", got)
	}
}

func TestOnlyIsOneRemovalAlone(t *testing.T) {
	removal := storageadd.LayoutStaging{Seen: true, Removed: []string{"8c2a5b1e0f3d4c7a"}}
	if !removal.Only("8c2a5b1e0f3d4c7a") || removal.Only("51494feb5444d466") {
		t.Error("one removal is that node's alone")
	}
	for _, s := range []storageadd.LayoutStaging{
		{Seen: true, Removed: []string{"8c2a5b1e0f3d4c7a", "51494feb5444d466"}},
		{Seen: true, Removed: []string{"8c2a5b1e0f3d4c7a"}, Assigned: []string{"0a1b2c3d4e5f6071"}},
		{Seen: true, Removed: []string{"8c2a5b1e0f3d4c7a"}, Parameters: true},
	} {
		if s.Only("8c2a5b1e0f3d4c7a") {
			t.Errorf("%#v is more than one removal", s)
		}
	}
	if (storageadd.LayoutStaging{}).Only("8c2a5b1e0f3d4c7a") {
		t.Error("nothing staged is not a removal")
	}
}
