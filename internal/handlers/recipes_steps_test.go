// Spec: specs/065-recipe-studio/spec.md
package handlers

import "testing"

func TestMarshalSteps_DropsBlankAndTrims(t *testing.T) {
	got := marshalSteps([]StepInput{
		{Text: " Sofría ", PhotoURL: ""},
		{Text: "", PhotoURL: ""}, // dropped (both blank)
		{Text: "Sirva", PhotoURL: " http://x/p.webp "},
	})
	want := `[{"text":"Sofría","photo_url":""},{"text":"Sirva","photo_url":"http://x/p.webp"}]`
	if got != want {
		t.Fatalf("marshalSteps = %s, want %s", got, want)
	}
}

func TestMarshalSteps_EmptyIsValidJSONArray(t *testing.T) {
	if got := marshalSteps(nil); got != "[]" {
		t.Fatalf("marshalSteps(nil) = %s, want []", got)
	}
}
