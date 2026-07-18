package codexcli

import "testing"

func TestTargetVersionValidation(t *testing.T) {
	for _, value := range []string{"0.144.4", "1.0.0", "12.34.56"} {
		if !ValidTargetVersion(value) {
			t.Fatalf("expected %q to be valid", value)
		}
	}
	for _, value := range []string{"v0.144.4", "0.144", "0.144.4-beta", "0.144.4; rm -rf /", "01.2.3"} {
		if ValidTargetVersion(value) {
			t.Fatalf("expected %q to be invalid", value)
		}
	}
}

func TestReportedVersionComparison(t *testing.T) {
	if !UpdateAvailable("codex-cli 0.139.0", "0.144.4") {
		t.Fatal("expected update to be available")
	}
	if UpdateAvailable("codex-cli 0.144.4", "0.144.4") || UpdateAvailable("codex-cli 0.145.0", "0.144.4") {
		t.Fatal("did not expect a downgrade or same-version update")
	}
	if !ReportedVersionMatches("codex-cli 0.144.4", "0.144.4") {
		t.Fatal("expected reported version to match")
	}
}
