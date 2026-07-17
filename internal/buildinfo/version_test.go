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
	if !UpdateAvailable("0.2.0", Version) {
		t.Fatalf("current version %s must be newer than deployed Agent 0.2.0", Version)
	}
}
