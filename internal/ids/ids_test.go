package ids

import "testing"

// The expected strings are copied from the workflows repo's tests/test_bond_selection.py
// (TestWorkflowIdKeysOnTheHubAndTheCandidatePool).
func TestPinnedToThePythonBuilders(t *testing.T) {
	cases := []struct{ got, want string }{
		{PoolSlot("ocp4-mce-tlv-01", "dell-r650-tlv-64c-1024gb", 1), "add-server-to-mce_ocp4-mce-tlv-01_dell-r650-tlv-64c-1024gb_1"},
		{Named("ocp4-mce-tlv-01", "ocp-dell-x"), "add-server-to-mce_ocp4-mce-tlv-01_ocp-dell-x"},
		{Named("ocp4-mce-tlv-01", "ocp-hp-gen11-nyc-64c-128gb-HP0001592"), "add-server-to-mce_ocp4-mce-tlv-01_ocp-hp-gen11-nyc-64c-128gb-hp0001592"},
		{PoolPrefix("ocp4-mce-tlv-01", "ie"), "add-server-to-mce_ocp4-mce-tlv-01_ie_"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestHyphensCannotMakeTwoPoolsShareAnId(t *testing.T) {
	if PoolSlot("ocp4-a", "b-c", 1) == PoolSlot("ocp4-a-b", "c", 1) {
		t.Fatal("MCE ocp4-a + InfraEnv b-c collides with MCE ocp4-a-b + InfraEnv c")
	}
}

func TestDifferentHubsAndSlotsDiffer(t *testing.T) {
	if PoolSlot("ocp4-a", "ie", 1) == PoolSlot("ocp4-b", "ie", 1) {
		t.Fatal("two hubs share a slot id")
	}
	if PoolSlot("ocp4-a", "ie", 1) == PoolSlot("ocp4-a", "ie", 2) {
		t.Fatal("two slots share an id")
	}
}
