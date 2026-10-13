package migrations

import (
	"fmt"
	"io/fs"
	"regexp"
	"testing"
)

func TestEveryMigrationHasUpAndDown(t *testing.T) {
	latest, err := Latest()
	if err != nil {
		t.Fatal(err)
	}
	for v := uint(1); v <= latest; v++ {
		for _, dir := range []string{"up", "down"} {
			matches, err := fs.Glob(FS, fmt.Sprintf("%06d_*.%s.sql", v, dir))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 1 {
				t.Errorf("version %d: want exactly one %s migration, found %v", v, dir, matches)
			}
		}
	}
}

func TestEmbeddedFilesFollowNamingConvention(t *testing.T) {
	names, err := sqlFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no migrations embedded")
	}
	for _, name := range names {
		if !fileName.MatchString(name) {
			t.Errorf("%q does not match NNNNNN_name.(up|down).sql", name)
		}
	}
}

func TestHashIsStableAndShort(t *testing.T) {
	a, err := Hash()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Hash()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("hash not deterministic: %s != %s", a, b)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("hash %q is not 12 lowercase hex characters", a)
	}
}
