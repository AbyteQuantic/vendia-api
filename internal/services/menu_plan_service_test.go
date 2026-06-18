// Spec: specs/066-planear-menu/spec.md
package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 2026-06-18 es jueves. Lo usamos como ancla de "hoy".
func thursday() time.Time {
	return time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
}

func dish(uuid string) MenuPlanItem { return MenuPlanItem{RecipeUUID: uuid, PlannedQty: 5} }

func TestResolveEffectiveMenu_TodayEnabled(t *testing.T) {
	days := map[string]DayPlan{
		"thu": {Enabled: true, Items: []MenuPlanItem{dish("r-thu")}},
	}
	got := ResolveEffectiveMenu(days, nil, thursday())

	assert.True(t, got.Found)
	assert.True(t, got.IsToday)
	assert.Equal(t, "thu", got.DayKey)
	assert.Len(t, got.Items, 1)
	assert.Equal(t, "", MenuDayLabel(got))
}

func TestResolveEffectiveMenu_TodayOffAdvancesToNext(t *testing.T) {
	days := map[string]DayPlan{
		"thu": {Enabled: false, Items: []MenuPlanItem{dish("r-thu")}}, // hoy apagado
		"fri": {Enabled: true, Items: []MenuPlanItem{dish("r-fri")}},  // mañana
	}
	got := ResolveEffectiveMenu(days, nil, thursday())

	assert.True(t, got.Found)
	assert.False(t, got.IsToday)
	assert.Equal(t, "fri", got.DayKey)
	assert.Equal(t, "r-fri", got.Items[0].RecipeUUID)
	assert.Equal(t, "Menú del viernes", MenuDayLabel(got))
}

func TestResolveEffectiveMenu_OverrideWinsOverTemplate(t *testing.T) {
	days := map[string]DayPlan{
		"thu": {Enabled: true, Items: []MenuPlanItem{dish("r-plantilla")}},
	}
	overrides := map[string]DayPlan{
		"2026-06-18": {Enabled: true, Items: []MenuPlanItem{dish("r-override")}},
	}
	got := ResolveEffectiveMenu(days, overrides, thursday())

	assert.True(t, got.Found)
	assert.True(t, got.IsToday)
	assert.Equal(t, "r-override", got.Items[0].RecipeUUID)
}

func TestResolveEffectiveMenu_OverrideDisablesDay(t *testing.T) {
	days := map[string]DayPlan{
		"thu": {Enabled: true, Items: []MenuPlanItem{dish("r-thu")}},
		"fri": {Enabled: true, Items: []MenuPlanItem{dish("r-fri")}},
	}
	overrides := map[string]DayPlan{
		"2026-06-18": {Enabled: false, Items: []MenuPlanItem{dish("r-thu")}}, // apaga hoy
	}
	got := ResolveEffectiveMenu(days, overrides, thursday())

	assert.True(t, got.Found)
	assert.False(t, got.IsToday)
	assert.Equal(t, "fri", got.DayKey) // salta al viernes
}

func TestResolveEffectiveMenu_EmptyWeekNotFound(t *testing.T) {
	got := ResolveEffectiveMenu(map[string]DayPlan{}, nil, thursday())
	assert.False(t, got.Found)
	assert.Empty(t, got.Items)
	assert.Equal(t, "", MenuDayLabel(got))
}

func TestResolveEffectiveMenu_EnabledButNoItemsSkips(t *testing.T) {
	days := map[string]DayPlan{
		"thu": {Enabled: true, Items: []MenuPlanItem{}},        // habilitado pero vacío
		"sat": {Enabled: true, Items: []MenuPlanItem{dish("r-sat")}},
	}
	got := ResolveEffectiveMenu(days, nil, thursday())
	assert.True(t, got.Found)
	assert.Equal(t, "sat", got.DayKey)
}

func TestRecipeUUIDSet_TrimsAndDropsEmpty(t *testing.T) {
	m := EffectiveMenu{Items: []MenuPlanItem{{RecipeUUID: " r1 "}, {RecipeUUID: ""}, {RecipeUUID: "r2"}}}
	set := RecipeUUIDSet(m)
	assert.Len(t, set, 2)
	_, ok := set["r1"]
	assert.True(t, ok)
}
