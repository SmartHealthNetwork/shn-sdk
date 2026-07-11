package health_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SmartHealthNetwork/shn-sdk/health"
)

func TestPollerCell_NilSafe(t *testing.T) {
	var c *health.PollerCell
	c.RecordAttempt() // must not panic
	c.RecordSuccess(3)
	c.RecordError(errors.New("x"))
}

func TestPollerCell_BootedIsOK(t *testing.T) {
	c := health.NewPollerCell("registrar-poller", 30*time.Second)
	got := c.Check(context.Background())
	if got.Status != health.StatusOK || got.Name != "registrar-poller" {
		t.Fatalf("pre-attempt check = %+v", got)
	}
}

func TestPollerCell_FirstFetchInFlightIsOK(t *testing.T) {
	c := health.NewPollerCell("p", time.Minute)
	c.RecordAttempt() // no outcome yet — boot window
	if got := c.Check(context.Background()); got.Status != health.StatusOK {
		t.Fatalf("in-flight first fetch must be ok: %+v", got)
	}
}

func TestPollerCell_SuccessThenError(t *testing.T) {
	c := health.NewPollerCell("registrar-poller", 30*time.Second)
	c.RecordAttempt()
	c.RecordSuccess(14)
	got := c.Check(context.Background())
	if got.Status != health.StatusOK || got.HolderCount != 14 || got.LastSuccess == "" {
		t.Fatalf("after success = %+v", got)
	}
	c.RecordAttempt()
	c.RecordError(errors.New("fetch holders: connection refused"))
	got = c.Check(context.Background())
	if got.Status != health.StatusDegraded || got.LastError == "" {
		t.Fatalf("after error = %+v", got)
	}
	// The last GOOD holderCount and lastSuccess stay visible alongside the error.
	if got.HolderCount != 14 || got.LastSuccess == "" {
		t.Fatalf("history lost on error = %+v", got)
	}
}

func TestPollerCell_ErrorStringIsCoarse(t *testing.T) {
	c := health.NewPollerCell("p", time.Minute)
	c.RecordAttempt()
	c.RecordError(errors.New(strings.Repeat("x", 500)))
	if got := c.Check(context.Background()); len(got.LastError) > 200 {
		t.Fatalf("lastError not truncated: %d chars", len(got.LastError))
	}
}

func TestPollerCell_StaleSuccessDegrades(t *testing.T) {
	c := health.NewPollerCell("p", 10*time.Millisecond)
	c.RecordAttempt()
	c.RecordSuccess(1)
	time.Sleep(30 * time.Millisecond)
	if got := c.Check(context.Background()); got.Status != health.StatusDegraded {
		t.Fatalf("stale success should degrade: %+v", got)
	}
}
