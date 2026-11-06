package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRequiresExplicitLocalDatabaseAndRedactsSecrets(t *testing.T) {
	c := Config{}
	r := &Runtime{config: c, dsn: "root:topsecret@tcp(127.0.0.1:3306)/demo"}
	if got := r.redact("dsn=" + r.dsn + " password=topsecret"); strings.Contains(got, "topsecret") {
		t.Fatal("credential disclosed")
	}
	for _, dsn := range []string{"", "root:pw@tcp(10.1.2.3:3306)/demo", "root:pw@tcp(127.0.0.1:3306)/"} {
		if _, err := parseLocalDSN(dsn); err == nil {
			t.Fatalf("unsafe DSN accepted: %q", dsn)
		}
	}
	a, _ := parseLocalDSN("u:one@tcp(127.0.0.1:3306)/a")
	b, _ := parseLocalDSN("u:two@tcp(127.0.0.1:3306)/a")
	if configKey(c, a) != configKey(c, b) {
		t.Fatal("password included in profile identity")
	}
	b.DBName = "b"
	if configKey(c, a) == configKey(c, b) {
		t.Fatal("different database treated as same profile")
	}
}
func TestStrictConfigRejectsUnknownFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"version":1,"command":"unsafe"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("unknown command field accepted")
	}
}
