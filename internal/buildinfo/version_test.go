package buildinfo

import "testing"

func TestSupportsSelfUpdate(t *testing.T) {
	for version, want := range map[string]bool{
		"0.1.9":  false,
		"0.2.0":  true,
		"v0.3.1": true,
		"dev":    false,
	} {
		if got := SupportsSelfUpdate(version); got != want {
			t.Fatalf("SupportsSelfUpdate(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestUpdateAvailableRequiresANewerTarget(t *testing.T) {
	for name, test := range map[string]struct {
		current string
		target  string
		want    bool
	}{
		"newer":       {current: "0.2.0", target: "0.3.0", want: true},
		"same":        {current: "0.2.0", target: "0.2.0", want: false},
		"downgrade":   {current: "0.3.0", target: "0.2.0", want: false},
		"unsupported": {current: "0.1.9", target: "0.3.0", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := UpdateAvailable(test.current, test.target); got != test.want {
				t.Fatalf("UpdateAvailable(%q, %q) = %v, want %v", test.current, test.target, got, test.want)
			}
		})
	}
}

func TestCurrentVersionUpdatesExistingSelfUpdatingAgents(t *testing.T) {
	for _, deployed := range []string{"0.2.43", "0.3.11"} {
		if !UpdateAvailable(deployed, Version) {
			t.Fatalf("current version %s must be newer than deployed Agent %s", Version, deployed)
		}
	}
}

func TestMinimumAgentVersion(t *testing.T) {
	for version, want := range map[string]bool{
		"": false, "invalid": false, "0.3.25": false, "0.3.9": false,
		"0.3.26": true, "v0.3.26+build": true, "0.3.27": true,
		"0.3.100": true, "0.4.0": true, "1.0.0": true, "-1.0.0": false,
	} {
		if got := MeetsAgentMinimum(version); got != want {
			t.Errorf("minimum %q: %v want %v", version, got, want)
		}
	}
	if !MeetsAgentMinimum(Version) {
		t.Fatal("built Agent is below baseline")
	}
}

func TestNumericVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		current, minimum string
		want             bool
	}{
		{"0.3.9", "0.3.26", false}, {"0.3.100", "0.3.26", true},
		{"0.10.0", "0.9.99", true}, {" v2.1.286+build ", "2.1.286", true},
		{"2.1.286-preview", "2.1.286", true}, {"2.1", "2.1.0", false},
		{"dev", "0.0.0", false}, {"1.0.0", "invalid", false},
	} {
		if got := VersionAtLeast(tc.current, tc.minimum); got != tc.want {
			t.Errorf("%q >= %q: %v", tc.current, tc.minimum, got)
		}
	}
}
