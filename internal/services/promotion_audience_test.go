// Spec: specs/033-difusion-promociones/spec.md
package services

import (
	"testing"
	"time"

	"vendia-backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// seedAudienceFixture builds a tenant with 10 customers covering every
// RFM profile and the sales that produce their aggregates. It returns
// the db and the tenant id. The "now" anchor is captured once so the
// 7/30-day windows are deterministic relative to it.
func seedAudienceFixture(t *testing.T) (*gorm.DB, string, time.Time) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, db.AutoMigrate(
		&models.Customer{},
		&models.Sale{},
	), "migrate")

	tenantID := "tenant-aud-1"
	otherTenant := "tenant-aud-2"
	now := time.Now().UTC()

	// Each customer: id, name, phone, and the sales we attach. A sale is
	// {amount, daysAgo}. The RFM expectations are documented per-row.
	type saleSpec struct {
		amount  float64
		daysAgo int
	}
	customers := []struct {
		id     string
		name   string
		phone  string
		sales  []saleSpec
		tenant string
	}{
		// 1. Maria — 4 purchases in last 30d, big spender.
		//    Frequent (>=3/30d), VIP candidate, Recent (last <=7d).
		{"c1", "Maria Frecuente", "3001000001", []saleSpec{
			{50000, 1}, {40000, 5}, {30000, 12}, {20000, 25},
		}, tenantID},
		// 2. Carlos — 3 purchases in 30d, last 6 days ago.
		//    Frequent, Recent.
		{"c2", "Carlos Frecuente", "3001000002", []saleSpec{
			{15000, 6}, {12000, 14}, {9000, 28},
		}, tenantID},
		// 3. Sofia — single huge purchase 3 days ago. VIP + Recent,
		//    NOT Frequent (only 1 purchase).
		{"c3", "Sofia VIP", "3001000003", []saleSpec{
			{200000, 3},
		}, tenantID},
		// 4. Andres — purchased 45 days ago only. Dormant.
		{"c4", "Andres Dormido", "3001000004", []saleSpec{
			{8000, 45},
		}, tenantID},
		// 5. Lucia — purchased 90 days ago only. Dormant.
		{"c5", "Lucia Dormida", "3001000005", []saleSpec{
			{6000, 90},
		}, tenantID},
		// 6. Pedro — never purchased. Dormant (purchase_count = 0).
		{"c6", "Pedro Nuevo", "3001000006", nil, tenantID},
		// 7. Diana — 2 purchases in 30d (NOT frequent), last 2 days
		//    ago (Recent).
		{"c7", "Diana Reciente", "3001000007", []saleSpec{
			{10000, 2}, {5000, 20},
		}, tenantID},
		// 8. Jorge — purchased 10 days ago. Not Recent, not Dormant,
		//    not Frequent. Only appears in "All".
		{"c8", "Jorge Medio", "3001000008", []saleSpec{
			{7000, 10},
		}, tenantID},
		// 9. Ana — has NO phone. Must be excluded from EVERY filter.
		{"c9", "Ana SinTelefono", "", []saleSpec{
			{30000, 2},
		}, tenantID},
		// 10. Other-tenant customer — must never leak into tenantID's
		//     audience.
		{"c10", "Intruso OtroTenant", "3009999999", []saleSpec{
			{99000, 1},
		}, otherTenant},
	}

	for _, cust := range customers {
		c := models.Customer{
			BaseModel: models.BaseModel{ID: cust.id},
			TenantID:  cust.tenant,
			Name:      cust.name,
			Phone:     cust.phone,
		}
		require.NoError(t, db.Create(&c).Error, "seed customer %s", cust.id)
		for i, s := range cust.sales {
			cid := cust.id
			sale := models.Sale{
				BaseModel: models.BaseModel{
					ID:        cust.id + "-sale-" + time.Duration(i).String(),
					CreatedAt: now.AddDate(0, 0, -s.daysAgo),
				},
				TenantID:   cust.tenant,
				Total:      s.amount,
				CustomerID: &cid,
			}
			require.NoError(t, db.Create(&sale).Error, "seed sale %s/%d", cust.id, i)
		}
	}

	return db, tenantID, now
}

// idsOf collects the customer ids from an audience result for set
// comparison regardless of order.
func idsOf(rows []AudienceCustomer) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestAudienceFilter_All(t *testing.T) {
	db, tenantID, now := seedAudienceFixture(t)

	rows, err := BuildAudience(db, tenantID, AudienceFilterAll, now)
	require.NoError(t, err)

	// Everyone with a phone in this tenant: c1..c8 (8). c9 has no
	// phone, c10 is another tenant.
	assert.ElementsMatch(t,
		[]string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"},
		idsOf(rows),
		"All = todos los del tenant con teléfono")
}

func TestAudienceFilter_Frequent(t *testing.T) {
	db, tenantID, now := seedAudienceFixture(t)

	rows, err := BuildAudience(db, tenantID, AudienceFilterFrequent, now)
	require.NoError(t, err)

	// >=3 purchases in the last 30 days: Maria (4) and Carlos (3).
	assert.ElementsMatch(t, []string{"c1", "c2"}, idsOf(rows),
		"Frequent = >=3 compras en 30d")
}

func TestAudienceFilter_VIP(t *testing.T) {
	db, tenantID, now := seedAudienceFixture(t)

	rows, err := BuildAudience(db, tenantID, AudienceFilterVIP, now)
	require.NoError(t, err)

	// Top 20% by total_spent among the 8 phone customers => ceil(0.2*8)
	// = 2. Highest spenders: Sofia (200k) and Maria (140k).
	assert.ElementsMatch(t, []string{"c1", "c3"}, idsOf(rows),
		"VIP = top 20% por gasto histórico")
}

func TestAudienceFilter_Dormant(t *testing.T) {
	db, tenantID, now := seedAudienceFixture(t)

	rows, err := BuildAudience(db, tenantID, AudienceFilterDormant, now)
	require.NoError(t, err)

	// No purchase in last 30d OR zero purchases: Andres (45d), Lucia
	// (90d), Pedro (never).
	assert.ElementsMatch(t, []string{"c4", "c5", "c6"}, idsOf(rows),
		"Dormant = sin compras 30d+ o sin compras")
}

func TestAudienceFilter_Recent(t *testing.T) {
	db, tenantID, now := seedAudienceFixture(t)

	rows, err := BuildAudience(db, tenantID, AudienceFilterRecent, now)
	require.NoError(t, err)

	// Purchase in the last 7 days: Maria (1d), Carlos (6d), Sofia (3d),
	// Diana (2d). Jorge (10d) is excluded.
	assert.ElementsMatch(t, []string{"c1", "c2", "c3", "c7"}, idsOf(rows),
		"Recent = compra en los últimos 7 días")
}

func TestAudienceFilter_ExcludesOtherTenantAndNoPhone(t *testing.T) {
	db, tenantID, now := seedAudienceFixture(t)

	for _, f := range []string{
		AudienceFilterAll, AudienceFilterFrequent, AudienceFilterVIP,
		AudienceFilterDormant, AudienceFilterRecent,
	} {
		rows, err := BuildAudience(db, tenantID, f, now)
		require.NoError(t, err, "filter %s", f)
		for _, r := range rows {
			assert.NotEqual(t, "c9", r.ID, "%s nunca incluye cliente sin teléfono", f)
			assert.NotEqual(t, "c10", r.ID, "%s nunca incluye otro tenant", f)
		}
	}
}

func TestAudienceFilter_InvalidFilter(t *testing.T) {
	db, tenantID, now := seedAudienceFixture(t)

	_, err := BuildAudience(db, tenantID, "no-existe", now)
	assert.Error(t, err, "un filtro desconocido debe fallar explícitamente")
}

func TestAudienceFilter_ReturnsAggregates(t *testing.T) {
	db, tenantID, now := seedAudienceFixture(t)

	rows, err := BuildAudience(db, tenantID, AudienceFilterAll, now)
	require.NoError(t, err)

	byID := map[string]AudienceCustomer{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	maria := byID["c1"]
	assert.EqualValues(t, 4, maria.PurchaseCount, "Maria tiene 4 compras")
	assert.EqualValues(t, 140000, maria.TotalSpent, "Maria gastó 140k")
	require.NotNil(t, maria.LastPurchaseAt, "Maria tiene last_purchase_at")

	pedro := byID["c6"]
	assert.EqualValues(t, 0, pedro.PurchaseCount, "Pedro sin compras")
	assert.Nil(t, pedro.LastPurchaseAt, "Pedro sin last_purchase_at")
}
