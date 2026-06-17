// Spec: specs/065-recipe-studio/spec.md
package services

import "testing"

func TestParseVoiceRecipeJSON_Object(t *testing.T) {
	raw := `{"name":"Arroz con pollo","ingredients":[{"name":"arroz","quantity":2,"unit":"libras"},{"name":"pollo","quantity":1,"unit":""}],"steps":["Sofría","Agregue el arroz"],"yield":"4 porciones"}`
	r, err := ParseVoiceRecipeJSON(raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r.Name != "Arroz con pollo" {
		t.Fatalf("name = %q", r.Name)
	}
	if len(r.Ingredients) != 2 || r.Ingredients[0].Name != "arroz" || r.Ingredients[0].Quantity != 2 {
		t.Fatalf("ingredients = %+v", r.Ingredients)
	}
	if len(r.Steps) != 2 {
		t.Fatalf("steps = %+v", r.Steps)
	}
	if r.Yield != "4 porciones" {
		t.Fatalf("yield = %q", r.Yield)
	}
}

func TestParseVoiceRecipeJSON_FencedAndStrayText(t *testing.T) {
	raw := "Claro, aquí está:\n```json\n{\"name\":\"Sopa\",\"ingredients\":[],\"steps\":[]}\n```\nEspero que ayude."
	r, err := ParseVoiceRecipeJSON(raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r.Name != "Sopa" {
		t.Fatalf("name = %q", r.Name)
	}
}

func TestParseVoiceRecipeJSON_ClampsAndCleans(t *testing.T) {
	raw := `{"name":" Lentejas ","ingredients":[{"name":"  ","quantity":1,"unit":"u"},{"name":"lenteja","quantity":-5,"unit":" libra "}],"steps":["  ","Cocine"]}`
	r, err := ParseVoiceRecipeJSON(raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r.Name != "Lentejas" {
		t.Fatalf("name not trimmed: %q", r.Name)
	}
	if len(r.Ingredients) != 1 {
		t.Fatalf("blank ingredient not dropped: %+v", r.Ingredients)
	}
	if r.Ingredients[0].Quantity != 0 {
		t.Fatalf("negative qty not clamped: %v", r.Ingredients[0].Quantity)
	}
	if r.Ingredients[0].Unit != "libra" {
		t.Fatalf("unit not trimmed: %q", r.Ingredients[0].Unit)
	}
	if len(r.Steps) != 1 || r.Steps[0] != "Cocine" {
		t.Fatalf("blank step not dropped: %+v", r.Steps)
	}
}

func TestParseVoiceRecipeJSON_Empty(t *testing.T) {
	r, err := ParseVoiceRecipeJSON("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r.Name != "" || len(r.Ingredients) != 0 || len(r.Steps) != 0 {
		t.Fatalf("expected empty result, got %+v", r)
	}
}
