// Spec: specs/023-capacidades-opcionales-negocio/spec.md
// Spec: specs/028-copy-fiar-credito-configurable/spec.md
// Spec: specs/029-precios-multi-tier/spec.md
// Spec: specs/030-administracion-clientes-no-tienda/spec.md
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// priceTierNameMaxLen mirrors the varchar(50) GORM constraint on the
// three Tenant.PriceTierNName columns. Surfacing the limit at the app
// layer keeps the 400 message in Spanish instead of a 500 from PG
// truncation. Spec F029 §5.
const priceTierNameMaxLen = 50

// GetBusinessProfile returns the current tenant's business profile data.
// GET /api/v1/store/profile
func GetBusinessProfile(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var tenant models.Tenant
		if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"business_name":          tenant.BusinessName,
				"business_types":         tenant.BusinessTypes,
				"feature_flags":          tenant.FeatureFlags,
				"nit":                    tenant.NIT,
				"razon_social":           tenant.RazonSocial,
				"address":                tenant.Address,
				"latitude":               tenant.Latitude,
				"longitude":              tenant.Longitude,
				"logo_url":               tenant.LogoURL,
				"owner_name":             tenant.OwnerName,
				"phone":                  tenant.Phone,
				"payment_method_name":    tenant.PaymentMethodName,
				"payment_account_number": tenant.PaymentAccountNumber,
				"payment_account_holder": tenant.PaymentAccountHolder,
				// Spec F028: vocabulary mode for "fiar"/"venta a crédito" copy.
				"credit_label_mode": tenant.CreditLabelMode,
				// Spec F029: precios multi-tier por tipo de cliente. El
				// frontend lee `enable_price_tiers` para decidir si pinta
				// el sub-formulario de nombres y el selector en POS, y los
				// 3 `price_tier_N_name` para etiquetar las opciones.
				"enable_price_tiers": tenant.EnablePriceTiers,
				"price_tier_1_name":  tenant.PriceTier1Name,
				"price_tier_2_name":  tenant.PriceTier2Name,
				"price_tier_3_name":  tenant.PriceTier3Name,
				// Spec F030: gestión de clientes. El frontend lee
				// `enable_customer_management` para decidir si pinta el
				// tile "Cliente" en el checkout y la entrada "Mis clientes"
				// en el menú principal.
				"enable_customer_management": tenant.EnableCustomerManagement,
			},
		})
	}
}

// ProfileConfigInput carries the optional capability toggles that a merchant
// can activate independently of their business type (Spec F023). Spec F029
// adds `enable_price_tiers` and the 3 tier-name overrides so the multi-tier
// pricing capability lives in the same config block as F023's toggles.
type ProfileConfigInput struct {
	HasTables      *bool `json:"has_tables"`
	OffersServices *bool `json:"offers_services"`
	SellsByWeight  *bool `json:"sells_by_weight"`

	// Spec F029 — precios multi-tier por tipo de cliente.
	EnablePriceTiers *bool   `json:"enable_price_tiers"`
	PriceTier1Name   *string `json:"price_tier_1_name"`
	PriceTier2Name   *string `json:"price_tier_2_name"`
	PriceTier3Name   *string `json:"price_tier_3_name"`

	// Spec F030 — gestión de clientes y ventas. Toggle opcional, default
	// OFF. Pointer para distinguir "no enviado" de "false explícito".
	EnableCustomerManagement *bool `json:"enable_customer_management"`
}

// UpdateBusinessProfile partially updates the tenant's business profile.
// When a config block is present, feature_flags are recomputed as
// (type-implied capabilities) OR (toggle values) — Spec F023 FR-07.
// Accepts optional credit_label_mode ("fiar"|"credit") — Spec F028 FR-02.
// PATCH /api/v1/store/profile
func UpdateBusinessProfile(db *gorm.DB) gin.HandlerFunc {
	type Request struct {
		BusinessName    *string             `json:"business_name"`
		BusinessTypes   []string            `json:"business_types"`
		NIT             *string             `json:"nit"`
		RazonSocial     *string             `json:"razon_social"`
		Address         *string             `json:"address"`
		Latitude        *float64            `json:"latitude"`
		Longitude       *float64            `json:"longitude"`
		Config          *ProfileConfigInput `json:"config"`
		CreditLabelMode *string             `json:"credit_label_mode"`
	}

	return func(c *gin.Context) {
		tenantID := middleware.GetTenantID(c)

		var req Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{}
		if req.BusinessName != nil {
			if *req.BusinessName == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "el nombre del negocio es obligatorio"})
				return
			}
			updates["business_name"] = *req.BusinessName
		}
		if req.BusinessTypes != nil {
			typesJSON, _ := json.Marshal(req.BusinessTypes)
			updates["business_types"] = string(typesJSON)
		}
		if req.NIT != nil {
			updates["nit"] = *req.NIT
		}
		if req.RazonSocial != nil {
			updates["razon_social"] = *req.RazonSocial
		}
		if req.Address != nil {
			updates["address"] = *req.Address
		}
		if req.Latitude != nil {
			updates["latitude"] = *req.Latitude
		}
		if req.Longitude != nil {
			updates["longitude"] = *req.Longitude
		}

		// Spec F028 FR-02: validate and persist credit_label_mode when provided.
		if req.CreditLabelMode != nil {
			mode := *req.CreditLabelMode
			if mode != "fiar" && mode != "credit" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "modo de etiqueta de crédito inválido: debe ser 'fiar' o 'credit'"})
				return
			}
			updates["credit_label_mode"] = mode
		}

		// Spec F023 FR-07: when a config block is present, recompute
		// feature_flags as (type-implied) OR (toggle values).
		if req.Config != nil {
			// Load the current tenant to get business_types and existing flags
			var tenant models.Tenant
			if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
				return
			}

			// Use request-provided business_types if being updated, else current
			businessTypes := tenant.BusinessTypes
			if req.BusinessTypes != nil {
				businessTypes = req.BusinessTypes
			}

			// Read toggles: use request value if provided, else derive from
			// current feature_flags vs the type baseline (D1: no new column).
			opts := resolveToggles(tenant, req.Config, businessTypes)

			flags := models.DefaultFeatureFlags(businessTypes, opts)
			flagsJSON, err := json.Marshal(flags)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "error al calcular capacidades"})
				return
			}
			updates["feature_flags"] = string(flagsJSON)
			updates["has_tables"] = flags.EnableTables

			// Spec F029 — precios multi-tier. Validate names FIRST so an
			// invalid name aborts the whole PATCH without leaving the
			// enable flag half-applied. trim, reject empty, enforce
			// varchar(50).
			if req.Config.PriceTier1Name != nil {
				name, err := validatePriceTierName(*req.Config.PriceTier1Name, "el nombre del tier 1")
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				updates["price_tier_1_name"] = name
			}
			if req.Config.PriceTier2Name != nil {
				name, err := validatePriceTierName(*req.Config.PriceTier2Name, "el nombre del tier 2")
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				updates["price_tier_2_name"] = name
			}
			if req.Config.PriceTier3Name != nil {
				name, err := validatePriceTierName(*req.Config.PriceTier3Name, "el nombre del tier 3")
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				updates["price_tier_3_name"] = name
			}
			if req.Config.EnablePriceTiers != nil {
				updates["enable_price_tiers"] = *req.Config.EnablePriceTiers
			}

			// Spec F030 — gestión de clientes. Toggle simple sin validación
			// adicional; sigue el mismo patrón que enable_price_tiers.
			if req.Config.EnableCustomerManagement != nil {
				updates["enable_customer_management"] = *req.Config.EnableCustomerManagement
			}
		}

		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no hay campos para actualizar"})
			return
		}

		if err := db.Model(&models.Tenant{}).Where("id = ?", tenantID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al actualizar perfil"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "perfil actualizado correctamente"})
	}
}

// resolveToggles builds a CapabilityToggles from the config input, using the
// request value when provided and falling back to the existing feature_flags
// state for omitted fields (Spec F023 D1 — no dedicated column; state
// derived from feature_flags vs the type baseline).
func resolveToggles(tenant models.Tenant, cfg *ProfileConfigInput, businessTypes []string) models.CapabilityToggles {
	// Compute the baseline implied by the type alone (opts all false)
	baseline := models.DefaultFeatureFlags(businessTypes, models.CapabilityToggles{})

	// For each toggle: use the explicit request value if given; otherwise
	// derive from "flag is ON and not implied by type" (current toggle state).
	tables := (tenant.FeatureFlags.EnableTables && !baseline.EnableTables)
	if cfg.HasTables != nil {
		tables = *cfg.HasTables
	}

	services := (tenant.FeatureFlags.EnableServices && !baseline.EnableServices)
	if cfg.OffersServices != nil {
		services = *cfg.OffersServices
	}

	fractional := (tenant.FeatureFlags.EnableFractionalUnits && !baseline.EnableFractionalUnits)
	if cfg.SellsByWeight != nil {
		fractional = *cfg.SellsByWeight
	}

	return models.CapabilityToggles{
		Tables:          tables,
		Services:        services,
		FractionalUnits: fractional,
	}
}

// validatePriceTierName normalises a tier label sent through the
// PATCH /api/v1/store/profile config block (Spec F029 FR-02):
//
//  1. trim surrounding whitespace,
//  2. reject empty (we never let a tier render as a blank label in the POS),
//  3. enforce the same 50-char cap the GORM column declares — surfaced
//     here in Spanish instead of bubbling up a PG truncation 500.
//
// label is the human-friendly noun used in the 400 message ("el nombre
// del tier 1", "el nombre del tier 2", ...) so the tendero knows which
// input to fix.
func validatePriceTierName(raw, label string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%s no puede quedar vacío", label)
	}
	if len(trimmed) > priceTierNameMaxLen {
		return "", fmt.Errorf("%s debe tener máximo %d caracteres", label, priceTierNameMaxLen)
	}
	return trimmed, nil
}
