package buildinfo

import "testing"

func TestGeminiYoloRequiresNewAgent(t *testing.T) {
	for version, want := range map[string]bool{"0.2.71": false, "0.2.72": true, "v0.3.0": true, "invalid": false} {
		if got := SupportsGeminiYolo(version); got != want {
			t.Errorf("SupportsGeminiYolo(%q)=%v, want %v", version, got, want)
		}
	}
}

func TestCLIManagementRequiresNewAgent(t *testing.T) {
	for version, want := range map[string]bool{"0.2.60": false, "0.2.61": true, "0.3.0": true, "invalid": false} {
		if SupportsCLIManagement(version) != want {
			t.Errorf("CLI management %s: want %v", version, want)
		}
	}
}

func TestSupportsGemini(t *testing.T) {
	for version, want := range map[string]bool{"0.2.59": false, "0.2.60": true, "0.2.61": true, "invalid": false} {
		if got := SupportsGemini(version); got != want {
			t.Fatalf("SupportsGemini(%q)=%v, want %v", version, got, want)
		}
	}
}

func TestSupportsMessageFork(t *testing.T) {
	for version, want := range map[string]bool{"0.2.58": false, "0.2.59": true, "0.2.60": true, "invalid": false} {
		if got := SupportsMessageFork(version); got != want {
			t.Fatalf("SupportsMessageFork(%q)=%v, want %v", version, got, want)
		}
	}
}

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
	if !UpdateAvailable("0.2.43", Version) {
		t.Fatalf("current version %s must be newer than deployed Agent 0.2.43", Version)
	}
}

func TestSupportsExactDeploymentRollback(t *testing.T) {
	for version, want := range map[string]bool{
		"0.2.44": false,
		"0.2.45": true,
		"v0.3.0": true,
		"dev":    false,
	} {
		if got := SupportsExactDeploymentRollback(version); got != want {
			t.Fatalf("SupportsExactDeploymentRollback(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestGeminiRewriteRequiresNewAgent(t *testing.T) {
	for version, want := range map[string]bool{"0.2.65": false, "0.2.66": true, "0.3.0": true, "invalid": false} {
		if SupportsGeminiRewrite(version) != want {
			t.Fatalf("Gemini rewrite %s: want %v", version, want)
		}
	}
}
