package services

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type InvoiceData struct {
	NIT       string `json:"nit,omitempty"`
	Total     string `json:"total,omitempty"`
	Date      string `json:"date,omitempty"`
	Vendor    string `json:"vendor,omitempty"`
	Method    string `json:"method"`
	RawItems  []any  `json:"raw_items,omitempty"`
}

type OCRService struct {
	apiKey string
}

func NewOCRService(apiKey string) *OCRService {
	return &OCRService{apiKey: apiKey}
}

var (
	nitRegex   = regexp.MustCompile(`\d{3}\.?\d{3}\.?\d{3}-?\d`)
	totalRegex = regexp.MustCompile(`(?i)(?:TOTAL|Total)\s*:?\s*\$?\s*([\d.,]+)`)
	dateRegex  = regexp.MustCompile(`\d{2}[/-]\d{2}[/-]\d{4}`)
)

func (s *OCRService) ProcessInvoice(imageData []byte, mimeType string) (*InvoiceData, error) {
	text := string(imageData)
	result := s.tryRegex(text)
	if result != nil {
		return result, nil
	}

	if s.apiKey == "" {
		return &InvoiceData{Method: "regex_partial"}, nil
	}

	return s.callGemini(imageData, mimeType)
}

func (s *OCRService) tryRegex(text string) *InvoiceData {
	data := &InvoiceData{Method: "regex"}
	fieldsFound := 0

	if m := nitRegex.FindString(text); m != "" {
		data.NIT = m
		fieldsFound++
	}
	if m := totalRegex.FindStringSubmatch(text); len(m) > 1 {
		data.Total = strings.TrimSpace(m[1])
		fieldsFound++
	}
	if m := dateRegex.FindString(text); m != "" {
		data.Date = m
		fieldsFound++
	}

	if fieldsFound >= 3 {
		return data
	}
	return nil
}

func (s *OCRService) callGemini(imageData []byte, mimeType string) (*InvoiceData, error) {
	b64 := base64.StdEncoding.EncodeToString(imageData)

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{
						"inlineData": map[string]any{
							"mimeType": mimeType,
							"data":     b64,
						},
					},
					{
						"text": "Extract from this Colombian invoice: nit, total, date, vendor. Return ONLY valid JSON: {\"nit\":\"\",\"total\":\"\",\"date\":\"\",\"vendor\":\"\"}",
					},
				},
			},
		},
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=%s", s.apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read gemini response: %w", err)
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse gemini response: %w", err)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty gemini response")
	}

	text := geminiResp.Candidates[0].Content.Parts[0].Text
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var result InvoiceData
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return &InvoiceData{Method: "gemini_flash", Vendor: text}, nil
	}

	result.Method = "gemini_flash"
	return &result, nil
}
