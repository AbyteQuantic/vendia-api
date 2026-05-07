package handlers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStartOfTenantDay_HandlesUTCRunningServer(t *testing.T) {
	// Production scenario: Render runs in UTC. It is 23:00 Bogotá on
	// May 6 — for the merchant's clock that's still "today". The
	// server-side time.Now() returns May 7 04:00 UTC. The previous
	// implementation truncated that to May 7 00:00 UTC, making sales
	// registered between 19:00 and 23:59 Bogotá invisible.
	serverNowUTC := time.Date(2026, 5, 7, 4, 0, 0, 0, time.UTC)
	start := startOfTenantDay(serverNowUTC)

	// The boundary must be Bogotá May 6 00:00 = May 6 05:00 UTC.
	expected := time.Date(2026, 5, 6, 5, 0, 0, 0, time.UTC)
	assert.True(t, start.Equal(expected),
		"startOfTenantDay shifted the cutoff: got %s, want %s",
		start.UTC(), expected.UTC())
}

func TestStartOfTenantDay_AfternoonInBogotaMatchesTodayUTC(t *testing.T) {
	// 15:00 Bogotá May 6 = 20:00 UTC May 6 — both UTC and Bogotá agree
	// on the calendar day. Should still yield Bogotá May 6 midnight.
	serverNowUTC := time.Date(2026, 5, 6, 20, 0, 0, 0, time.UTC)
	start := startOfTenantDay(serverNowUTC)
	expected := time.Date(2026, 5, 6, 5, 0, 0, 0, time.UTC)
	assert.True(t, start.Equal(expected))
}

func TestStartOfTenantDay_PreDawnBogotaStillUsesPreviousCalendarDay(t *testing.T) {
	// 02:00 Bogotá May 6 = 07:00 UTC May 6. Bogotá calendar still says
	// May 6, so startOfTenantDay must be May 6 00:00 Bogotá, NOT
	// May 5 00:00 Bogotá.
	serverNowUTC := time.Date(2026, 5, 6, 7, 0, 0, 0, time.UTC)
	start := startOfTenantDay(serverNowUTC)
	expected := time.Date(2026, 5, 6, 5, 0, 0, 0, time.UTC)
	assert.True(t, start.Equal(expected))
}

func TestSaleAt23BogotaIsInTodayRange(t *testing.T) {
	// The PO's exact regression: a sale registered at 23:00 Bogotá
	// May 6 should fall inside the "today" window queried at 23:30
	// Bogotá the same day.
	saleAtUTC := time.Date(2026, 5, 7, 4, 0, 0, 0, time.UTC)    // 23:00 Bogotá May 6
	queryNowUTC := time.Date(2026, 5, 7, 4, 30, 0, 0, time.UTC) // 23:30 Bogotá May 6

	start := startOfTenantDay(queryNowUTC)

	assert.True(t, saleAtUTC.After(start) || saleAtUTC.Equal(start),
		"sale at 23:00 Bogotá should fall inside today's range starting at %s",
		start.UTC())
	assert.True(t, saleAtUTC.Before(queryNowUTC),
		"sanity: sale must precede the query moment")
}

func TestTenantNow_AppliesBogotaOffset(t *testing.T) {
	// Smoke test: tenantNow() and time.Now().UTC() must encode the same
	// instant (modulo a few microseconds) but the wall clock must show
	// the -5h offset because Colombia has no DST.
	n := tenantNow()
	u := time.Now().UTC()
	diff := u.Sub(n.UTC())
	if diff < 0 {
		diff = -diff
	}
	assert.Less(t, diff, time.Second,
		"tenantNow and time.Now should encode the same instant")
	_, offset := n.Zone()
	assert.Equal(t, -5*60*60, offset,
		"tenant timezone must be UTC-5 (no DST)")
}

func TestStartOfTenantDay_BoundaryIsExclusiveOfPreviousDay(t *testing.T) {
	// A sale at 23:59:59 Bogotá May 5 (= 04:59:59 UTC May 6) must NOT
	// be in May 6's range.
	saleAtUTC := time.Date(2026, 5, 6, 4, 59, 59, 0, time.UTC)  // 23:59:59 Bogotá May 5
	queryNowUTC := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) // 07:00 Bogotá May 6

	start := startOfTenantDay(queryNowUTC)

	assert.True(t, saleAtUTC.Before(start),
		"sale at 23:59:59 Bogotá May 5 should be before today's start at %s",
		start.UTC())
}
