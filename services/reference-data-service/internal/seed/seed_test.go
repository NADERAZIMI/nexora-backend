package seed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nexora-exchange/nexora/libs/go/refdata"
)

func TestFixtureIsValidAndDeterministic(t *testing.T) {
	document := Document()
	if issues := document.Validate(); len(issues) > 0 {
		t.Fatalf("fixture validation failed: %+v", issues)
	}
	first, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	second, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("fixture canonical bytes are not deterministic")
	}
	if len(refdata.ContentHash(first)) != 64 {
		t.Fatal("fixture hash is not SHA-256")
	}
	t.Logf("canonical fixture bytes: %d, sha256: %s", len(first), refdata.ContentHash(first))
}

// TestGoldenFixture keeps the Go canonical form and the committed cross-language
// fixture byte-identical. Run with UPDATE_GOLDEN=1 after an intentional schema
// change.
func TestGoldenFixture(t *testing.T) {
	canonical, err := Document().CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "..", "..")
	fixturePath := filepath.Join(root, "contracts", "fixtures", "reference-data", "v1", "snapshot.json")
	hashPath := filepath.Join(root, "contracts", "fixtures", "reference-data", "v1", "snapshot.sha256")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixturePath, canonical, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hashPath, []byte(refdata.ContentHash(canonical)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	if string(committed) != string(canonical) {
		t.Fatalf("golden fixture differs from canonical seed document; rerun with UPDATE_GOLDEN=1")
	}
	committedHash, err := os.ReadFile(hashPath)
	if err != nil {
		t.Fatalf("read golden hash: %v", err)
	}
	if refdata.ContentHash(canonical)+"\n" != string(committedHash) {
		t.Fatal("golden fixture hash does not match canonical content")
	}
}
