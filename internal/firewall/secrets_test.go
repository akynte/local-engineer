package firewall_test

import (
	"github.com/akynte/local-engineer/internal/firewall"
	"strings"
	"testing"
)

func TestDiffSecretsOnlyChecksAddedContentAndRedacts(t *testing.T) {
	secret := "AKIA" + strings.Repeat("X", 16)
	if err := firewall.CheckDiffSecrets("+++ b/config.go\n+token=" + secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("missing or disclosing finding: %v", err)
	}
	if err := firewall.CheckDiffSecrets("+++ b/config.go\n-token=" + secret + "\n+token=loadFromEnvironment()"); err != nil {
		t.Fatal(err)
	}
}

// A memory note never reaches the finalization diff, so the content check is
// the only thing standing between a credential the model read and every later
// prompt that replays the note.
func TestContentSecretsNamesTheRouteWithoutRepeatingTheCredential(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 36)
	err := firewall.CheckContentSecrets("this note", "use "+secret+" to reach staging")
	if err == nil {
		t.Fatal("credential in stored content was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the finding repeats the credential: %v", err)
	}
	if !strings.Contains(err.Error(), "this note") {
		t.Fatalf("the finding does not name the route: %v", err)
	}
	if err := firewall.CheckContentSecrets("this note", "the staging token lives in $STAGING_TOKEN"); err != nil {
		t.Fatal(err)
	}
}
