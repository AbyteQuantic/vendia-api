package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTenantSubscription_IsPremium(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		sub  *TenantSubscription
		want bool
	}{
		{
			name: "nil subscription is never premium",
			sub:  nil,
			want: false,
		},
		{
			name: "PRO_ACTIVE passes regardless of trial end",
			sub:  &TenantSubscription{Status: SubscriptionStatusProActive},
			want: true,
		},
		{
			name: "TRIAL with future trial_ends_at passes",
			sub: &TenantSubscription{
				Status:      SubscriptionStatusTrial,
				TrialEndsAt: ptrTime(now.Add(2 * 24 * time.Hour)),
			},
			want: true,
		},
		{
			name: "TRIAL with past trial_ends_at fails",
			sub: &TenantSubscription{
				Status:      SubscriptionStatusTrial,
				TrialEndsAt: ptrTime(now.Add(-1 * time.Hour)),
			},
			want: false,
		},
		{
			name: "TRIAL with nil trial_ends_at fails (corrupted state)",
			sub: &TenantSubscription{
				Status:      SubscriptionStatusTrial,
				TrialEndsAt: nil,
			},
			want: false,
		},
		{
			name: "FREE never passes",
			sub:  &TenantSubscription{Status: SubscriptionStatusFree},
			want: false,
		},
		{
			name: "PRO_PAST_DUE never passes (needs explicit upgrade back to active)",
			sub:  &TenantSubscription{Status: SubscriptionStatusProPastDue},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.sub.IsPremium(now))
		})
	}
}

func TestTenantSubscription_TrialDaysRemaining(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		sub  *TenantSubscription
		want int
	}{
		{
			name: "nil returns 0",
			sub:  nil,
			want: 0,
		},
		{
			name: "PRO_ACTIVE returns 0 (not a trial)",
			sub: &TenantSubscription{
				Status:      SubscriptionStatusProActive,
				TrialEndsAt: ptrTime(now.Add(7 * 24 * time.Hour)),
			},
			want: 0,
		},
		{
			name: "TRIAL with 7 days exactly returns 7",
			sub: &TenantSubscription{
				Status:      SubscriptionStatusTrial,
				TrialEndsAt: ptrTime(now.Add(7 * 24 * time.Hour)),
			},
			want: 7,
		},
		{
			name: "TRIAL with 6.5 days rounds up to 7 (UX-forgiving)",
			sub: &TenantSubscription{
				Status:      SubscriptionStatusTrial,
				TrialEndsAt: ptrTime(now.Add(6*24*time.Hour + 12*time.Hour)),
			},
			want: 7,
		},
		{
			name: "TRIAL with 0.1 days rounds up to 1",
			sub: &TenantSubscription{
				Status:      SubscriptionStatusTrial,
				TrialEndsAt: ptrTime(now.Add(2 * time.Hour)),
			},
			want: 1,
		},
		{
			name: "TRIAL with expired end returns 0",
			sub: &TenantSubscription{
				Status:      SubscriptionStatusTrial,
				TrialEndsAt: ptrTime(now.Add(-1 * time.Hour)),
			},
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.sub.TrialDaysRemaining(now))
		})
	}
}

func TestValidSubscriptionStatuses_ContainsAllConstants(t *testing.T) {
	for _, s := range []string{
		SubscriptionStatusTrial,
		SubscriptionStatusFree,
		SubscriptionStatusProActive,
		SubscriptionStatusProPastDue,
	} {
		_, ok := ValidSubscriptionStatuses[s]
		assert.True(t, ok, "status %q missing from ValidSubscriptionStatuses", s)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
