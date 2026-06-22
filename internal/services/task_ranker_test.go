// Spec: specs/078-centro-tareas-unificado/spec.md
package services_test

import (
	"context"
	"errors"
	"testing"

	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/stretchr/testify/assert"
)

func ids(ts []models.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func sample() []models.Task {
	return []models.Task{
		{ID: "reorder:t", Urgency: "normal", Title: "Reordenar"},
		{ID: "online_order:1", Urgency: "critical", Title: "Pedido"},
		{ID: "errand:9", Urgency: "normal", Title: "Mandado"},
	}
}

func TestRankTasks_ReordersByAI(t *testing.T) {
	gen := func(ctx context.Context, prompt string) (string, error) {
		return "```json\n{\"order\":[\"online_order:1\",\"errand:9\",\"reorder:t\"]}\n```", nil
	}
	got := services.RankTasks(sample(), 13, gen)
	assert.Equal(t, []string{"online_order:1", "errand:9", "reorder:t"}, ids(got))
}

func TestRankTasks_FallbackOnError(t *testing.T) {
	gen := func(ctx context.Context, prompt string) (string, error) { return "", errors.New("timeout") }
	got := services.RankTasks(sample(), 13, gen)
	assert.Equal(t, ids(sample()), ids(got)) // orden original
}

func TestRankTasks_FallbackOnGarbage(t *testing.T) {
	gen := func(ctx context.Context, prompt string) (string, error) { return "lo siento, no sé", nil }
	got := services.RankTasks(sample(), 13, gen)
	assert.Equal(t, ids(sample()), ids(got))
}

func TestRankTasks_PartialOrderAppendsMissing(t *testing.T) {
	// IA solo menciona una; las demás se anexan en su orden original.
	gen := func(ctx context.Context, prompt string) (string, error) {
		return "{\"order\":[\"online_order:1\"]}", nil
	}
	got := services.RankTasks(sample(), 13, gen)
	assert.Equal(t, "online_order:1", got[0].ID)
	assert.Len(t, got, 3)
}

func TestRankTasks_NoopUnderTwo(t *testing.T) {
	one := []models.Task{{ID: "a", Urgency: "normal"}}
	called := false
	gen := func(ctx context.Context, prompt string) (string, error) { called = true; return "", nil }
	got := services.RankTasks(one, 13, gen)
	assert.Equal(t, one, got)
	assert.False(t, called) // no llama a la IA con <2 tareas
}
