package smklog_test

import (
	"context"
	"os"
	"testing"
	"time"

	smklog "github.com/smklog/smklog-sdk/go"
)

// The one test that reaches production, and only its liveness endpoint: a
// quote costs money, so nothing in this suite ever calls GetParcelQuote live.
func TestLiveStatus(t *testing.T) {
	if os.Getenv("SMKLOG_LIVE") != "1" {
		t.Skip("set SMKLOG_LIVE=1 to call GET /status on production")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	live, err := smklog.New().Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !live.Ok || live.Service != "smklog-quote-api" || live.Version == "" || live.Time == "" {
		t.Errorf("liveness = %+v", live)
	}
}
