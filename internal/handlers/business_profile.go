// Spec: specs/023-capacidades-opcionales-negocio/spec.md
// Spec: specs/028-copy-fiar-credito-configurable/spec.md
package handlers

import (
	"encoding/json"
	"net/http"
	"vendia-backend/internal/middleware"
	"vendia-backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

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
			},
		})
	}
}

// ProfileConfigInput carries the optional capability toggles that a merchant
// can activate independently of their business type (Spec F023).
type ProfileConfigInput struct {
	HasTables      *bool `json:"has_tables"`
	OffersServices *bool `json:"offers_services"`
	SellsByWeight  *bool `json:"sells_by_weight"`
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
