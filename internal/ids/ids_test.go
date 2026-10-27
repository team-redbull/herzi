package ids

import (
	"strings"
	"testing"
)

// The expected strings are copied from the workflows repo's tests/test_bond_selection.py
// (TestWorkflowIdKeysOnTheHubAndTheCandidatePool).
func TestPinnedToThePythonBuilders(t *testing.T) {
	cases := []struct{ got, want string }{
		{PoolSlot("ocp4-mce-tlv-01", "dell-r650-tlv-64c-1024gb", 1), "install-server-ocp4-mce-tlv-01_dell-r650-tlv-64c-1024gb_1"},
		{Named("ocp4-mce-tlv-01", "ocp-dell-x"), "install-server-ocp4-mce-tlv-01_ocp-dell-x"},
		{Named("ocp4-mce-tlv-01", "ocp-hp-gen11-nyc-64c-128gb-HP0001592"), "install-server-ocp4-mce-tlv-01_ocp-hp-gen11-nyc-64c-128gb-hp0001592"},
		{PoolPrefix("ocp4-mce-tlv-01", "ie"), "install-server-ocp4-mce-tlv-01_ie_"},
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
	if PoolSlot("ocp4-mce-tlv-01", "dell-r650-tlv-64c-1024gb", 1) == PoolSlot("ocp4-mce-tlv-01", "hp-gen11-nyc-64c-128gb", 1) {
		t.Fatal("two InfraEnvs share an id")
	}
	if PoolSlot("ocp4-a", "ie", 1) == PoolSlot("ocp4-b", "ie", 1) {
		t.Fatal("two hubs share a slot id")
	}
	if PoolSlot("ocp4-a", "ie", 1) == PoolSlot("ocp4-a", "ie", 2) {
		t.Fatal("two slots share an id")
	}
}

func TestHubPrefixDoesNotMatchExtendedHubName(t *testing.T) {
	prefix := "install-server-ocp4-a_"
	if !strings.HasPrefix(PoolSlot("ocp4-a", "ie", 1), prefix) {
		t.Fatal("missing hub prefix")
	}
	for _, id := range []string{PoolSlot("ocp4-a-b", "ie", 1), Named("ocp4-a-b", "ocp-x")} {
		if strings.HasPrefix(id, prefix) {
			t.Fatal("extended hub matched prefix")
		}
	}
}
func TestEveryRunOfHubSharesHubPrefix(t *testing.T) {
	for _, id := range []string{PoolSlot("ocp4-mce-tlv-01", "ie", 3), Named("ocp4-mce-tlv-01", "ocp-dell-x")} {
		if !strings.HasPrefix(id, "install-server-ocp4-mce-tlv-01_") {
			t.Fatal(id)
		}
	}
}
func TestNamedServerCaseDoesNotChangeID(t *testing.T) {
	if Named("ocp4-mce-tlv-01", "ocp-hp-gen11-nyc-64c-128gb-HP0001592") != Named("ocp4-mce-tlv-01", "ocp-hp-gen11-nyc-64c-128gb-hp0001592") {
		t.Fatal("case changed id")
	}
}
func TestDifferentNamedServersDiffer(t *testing.T) {
	if Named("ocp4-mce-tlv-01", "ocp-a-HP0001592") == Named("ocp4-mce-tlv-01", "ocp-a-HP0001593") {
		t.Fatal("different servers collided")
	}
}
func TestNamedAndPoolIDsDiffer(t *testing.T) {
	if Named("ocp4-mce-tlv-01", "ocp-dell-x") == PoolSlot("ocp4-mce-tlv-01", "dell-r650-tlv-64c-1024gb", 1) {
		t.Fatal("named id took a pool slot")
	}
}
func TestPoolPrefixMatchesOnlyItsPool(t *testing.T) {
	prefix := PoolPrefix("ocp4-a", "ie")
	for _, slot := range []int{1, 2, 100} {
		if !strings.HasPrefix(PoolSlot("ocp4-a", "ie", slot), prefix) {
			t.Fatal("missing pool prefix")
		}
	}
	for _, id := range []string{PoolSlot("ocp4-a", "ie-other", 1), PoolSlot("ocp4-a-b", "ie", 1), Named("ocp4-a", "ocp-x")} {
		if strings.HasPrefix(id, prefix) {
			t.Fatal("unrelated run matched pool prefix")
		}
	}
}
