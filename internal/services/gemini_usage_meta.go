package services

// GemUsageMetadata mirrors Google Generative Language API JSON. Some
// responses use camelCase; protobuf JSON codecs may emit snake_case.
// https://ai.google.dev/api/generate-content#UsageMetadata
type GemUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
	PromptTokenCountAlt  int `json:"prompt_token_count"`
	CandidatesTokenAlt   int `json:"candidates_token_count"`
	TotalTokenCountAlt   int `json:"total_token_count"`
}

// InputOutput returns best-effort prompt/candidate token counts for billing.
func (m GemUsageMetadata) InputOutput() (in, out int) {
	in = m.PromptTokenCount
	if in == 0 {
		in = m.PromptTokenCountAlt
	}
	out = m.CandidatesTokenCount
	if out == 0 {
		out = m.CandidatesTokenAlt
	}
	tot := m.TotalTokenCount
	if tot == 0 {
		tot = m.TotalTokenCountAlt
	}
	if in == 0 && out == 0 && tot > 0 {
		// Provider sometimes only fills totalTokenCount
		out = tot
	}
	return in, out
}
