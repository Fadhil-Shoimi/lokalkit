package updater

import (
	"context"
	"os"
	"testing"
)

// Run with LOKALKIT_NET=1 to check the real release feed.
func TestRealFeed(t *testing.T) {
	if os.Getenv("LOKALKIT_NET") == "" {
		t.Skip("set LOKALKIT_NET=1")
	}
	rel, err := Latest(context.Background(), FeedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("latest=%s sha256=%v newer than 0.1.0-dev: %v", rel.Version, rel.SHA256 != "", Newer(rel.Version, "0.1.0-dev"))
}
