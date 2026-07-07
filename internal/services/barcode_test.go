// Spec: specs/098-aporte-automatico-catalogo/spec.md — Fase 2.
package services

import "testing"

func TestParseVerifyMatch(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantOK   bool
		wantConf float64
	}{
		{"match alto", `{"match":true,"confidence":0.95}`, true, 0.95},
		{"con fences markdown", "```json\n{\"match\":true,\"confidence\":0.8}\n```", true, 0.8},
		{"no match", `{"match":false,"confidence":0.9}`, false, 0.9},
		{"confianza clamp >1", `{"match":true,"confidence":1.7}`, true, 1},
		{"confianza clamp <0", `{"match":true,"confidence":-0.2}`, true, 0},
		{"json ilegible → fail-safe", `no soy json`, false, 0},
		{"vacío → fail-safe", ``, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, conf := parseVerifyMatch(tt.raw)
			if ok != tt.wantOK || conf != tt.wantConf {
				t.Errorf("parseVerifyMatch(%q) = (%v, %v), want (%v, %v)", tt.raw, ok, conf, tt.wantOK, tt.wantConf)
			}
		})
	}
}

func TestValidRetailBarcode(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		// Válidos: dígito de control GTIN mod-10 correcto.
		{"EAN-13 real (7702005004467)", "7702005004467", true},
		{"EAN-13 real (7501031311309)", "7501031311309", true},
		{"UPC-A / 12 dígitos", "036000291452", true},
		{"EAN-8", "96385074", true},
		{"ITF-14", "17702005004464", true},
		{"EAN-13 con espacios alrededor", "  7702005004467  ", true},

		// Inválidos.
		{"checksum malo (EAN-13)", "7702005004460", false},
		{"checksum malo (UPC-A)", "036000291453", false},
		{"longitud no estándar 5", "12345", false},
		{"longitud no estándar 10", "1234567890", false},
		{"vacío", "", false},
		{"SKU interno con letras", "VND-123", false},
		{"no-dígitos misma longitud", "7702ABC004467", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidRetailBarcode(tt.code); got != tt.want {
				t.Errorf("ValidRetailBarcode(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}
