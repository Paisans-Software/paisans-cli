package render

import (
	"fmt"
	"testing"
)

func TestSettingListAndIntReadTheSeenSettings(t *testing.T) {
	v := appValues{set: map[string]any{"groups": []any{"admins", "members"}, "mb": 12}}
	if got := fmt.Sprint(v.SettingList("groups")); got != "[admins members]" {
		t.Errorf("SettingList = %s", got)
	}
	if v.SettingList("absent") != nil {
		t.Error("an absent list is not nil")
	}
	if v.SettingInt("mb", 5) != 12 || v.SettingInt("absent", 5) != 5 {
		t.Error("SettingInt does not read the setting or its fallback")
	}
}
