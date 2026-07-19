// Spec: specs/023-capacidades-opcionales-negocio/spec.md
// Spec: specs/028-copy-fiar-credito-configurable/spec.md
// Spec: specs/029-precios-multi-tier/spec.md
// Spec: specs/030-administracion-clientes-no-tienda/spec.md
// Spec: specs/037-reel-capacidades-dashboard/spec.md
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
				"store_tagline":          tenant.StoreTagline,
				"brand_color":            tenant.BrandColor,
				"store_hours":            tenant.StoreHours,
				"store_cover_url":        tenant.StoreCoverURL,
				"category_order":         tenant.CategoryOrder,
				"store_slug":             tenant.StoreSlug,
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
				// Spec F031: módulo de cotizaciones. El frontend lee
				// `enable_quotes` para decidir si pinta la entrada
				// "Cotizaciones" en el menú principal.
				"enable_quotes": tenant.EnableQuotes,
				// Spec F033: módulo de difusión de promociones. El frontend
				// lee `enable_promotions` para decidir si pinta la entrada
				// "Promociones" en el menú principal.
				"enable_promotions": tenant.EnablePromotions,
				// Spec F036: bandera del onboarding. El frontend la lee
				// tras el login; si es `false`, muestra el wizard de
				// onboarding antes del Dashboard.
				"onboarding_completed": tenant.OnboardingCompleted,
				// Spec F037 — capacidades movidas de byType→opcional.
				// El frontend lee estos flags para decidir si pinta las
				// cards correspondientes en el Dashboard (Marketing Hub,
				// Recetas, Insumos, Trabajos de Muebles, Órdenes de
				// Compra). Default false; activables desde el reel.
				"enable_marketing_hub":   tenant.EnableMarketingHub,
				"enable_recipes":         tenant.EnableRecipes,
				"enable_supplies":        tenant.EnableSupplies,
				"enable_furniture_jobs":  tenant.EnableFurnitureJobs,
				"enable_purchase_orders": tenant.EnablePurchaseOrders,
				"hide_offers_section":    tenant.HideOffersSection,
				// Spec 095: variantes de producto (talla/color). El frontend
				// lee `enable_product_variants` para decidir si muestra el
				// generador de variantes en Nuevo/Editar Producto.
				"enable_product_variants": tenant.EnableProductVariants,
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

	// Spec F031 — módulo de cotizaciones. Toggle opcional, default OFF.
	// Pointer para distinguir "no enviado" de "false explícito".
	EnableQuotes *bool `json:"enable_quotes"`

	// Spec F033 — módulo de difusión de promociones. Toggle opcional,
	// default OFF. Pointer para distinguir "no enviado" de "false
	// explícito".
	EnablePromotions *bool `json:"enable_promotions"`

	// Spec F042 — módulo de eventos. Self-activado por el tendero desde el
	// reel "Descubre más opciones" (decisión #2). Vive dentro de
	// feature_flags (no es columna top-level), así que se preserva
	// explícitamente en el recompute. Default OFF.
	EnableEvents *bool `json:"enable_events"`

	// Spec 105 F3 — "el mesero puede cobrar". Default OFF (mesero puro).
	EnableWaiterCharge *bool `json:"enable_waiter_charge"`

	// Spec F037 — capacidades reclasificadas de byType→opcional. Cada
	// flag es un toggle simple que el reel del Dashboard puede activar
	// vía PATCH. Default OFF. Pointer para distinguir "no enviado" de
	// "false explícito".
	EnableMarketingHub   *bool `json:"enable_marketing_hub"`
	EnableRecipes        *bool `json:"enable_recipes"`
	EnableSupplies       *bool `json:"enable_supplies"`
	EnableFurnitureJobs  *bool `json:"enable_furniture_jobs"`
	EnablePurchaseOrders *bool `json:"enable_purchase_orders"`
	// Oculta la sección de Ofertas del catálogo público (Marketing Hub).
	HideOffersSection *bool `json:"hide_offers_section"`

	// Spec 095 — variantes de producto (talla/color). Toggle opcional,
	// default OFF. Mismo patrón que enable_price_tiers: pointer para
	// distinguir "no enviado" de "false explícito".
	EnableProductVariants *bool `json:"enable_product_variants"`
}

// UpdateBusinessProfile partially updates the tenant's business profile.
// When a config block is present, feature_flags are recomputed as
// (type-implied capabilities) OR (toggle values) — Spec F023 FR-07.
// Accepts optional credit_label_mode ("fiar"|"credit") — Spec F028 FR-02.
// PATCH /api/v1/store/profile
// sanitizeHexColor acepta "" (limpiar) o un hex "#RRGGBB"/"#AARRGGBB"; cualquier
// otra cosa se descarta (devuelve "") para no guardar basura en brand_color.
func sanitizeHexColor(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if s[0] != '#' || (len(s) != 7 && len(s) != 9) {
		return ""
	}
	for _, r := range s[1:] {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHex {
			return ""
		}
	}
	return s
}

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
		// Spec 082 — personalización del catálogo online.
		StoreTagline  *string `json:"store_tagline"`
		BrandColor    *string `json:"brand_color"`
		StoreHours    *string `json:"store_hours"`
		StoreCoverURL *string `json:"store_cover_url"`

		// Spec F036 — bandera del onboarding. El wizard la manda en
		// `true` al terminar o al saltarse ("Configurar después").
		// Pointer para distinguir "no enviado" de "false explícito".
		OnboardingCompleted *bool `json:"onboarding_completed"`
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

			// F042: el perfil cambia los tipos pero NO manda bloque `config`
			// (F036 separó perfil de capacidades). Aun así, las capacidades
			// IMPLÍCITAS por tipo deben aplicarse — academias → eventos —, o
			// nunca se encenderían al elegir el tipo. Solo AÑADE (nunca apaga).
			if req.Config == nil {
				for _, t := range req.BusinessTypes {
					if t == models.BusinessTypeAcademias {
						var current models.Tenant
						if err := db.Where("id = ?", tenantID).First(&current).Error; err == nil {
							flags := current.FeatureFlags
							flags.EnableEvents = true
							if fj, err := json.Marshal(flags); err == nil {
								updates["feature_flags"] = string(fj)
							}
						}
						break
					}
				}
			}
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
		// Spec 082 — eslogan + color de marca del catálogo (whitelist suave del
		// color: solo hex "#RRGGBB"/"#AARRGGBB" o vacío para limpiar).
		if req.StoreTagline != nil {
			updates["store_tagline"] = *req.StoreTagline
		}
		if req.BrandColor != nil {
			updates["brand_color"] = sanitizeHexColor(*req.BrandColor)
		}
		if req.StoreHours != nil {
			updates["store_hours"] = *req.StoreHours
		}
		if req.StoreCoverURL != nil {
			updates["store_cover_url"] = *req.StoreCoverURL
		}
		if req.Latitude != nil {
			updates["latitude"] = *req.Latitude
		}
		if req.Longitude != nil {
			updates["longitude"] = *req.Longitude
		}

		// Spec F036: persist the onboarding flag when the wizard sends it.
		if req.OnboardingCompleted != nil {
			updates["onboarding_completed"] = *req.OnboardingCompleted
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

		// Spec 106 (fix T-11): when a config block is present, START from the
		// tenant's CURRENT feature_flags and apply ONLY the explicit toggles.
		// The old behavior re-derived the matrix from business_types
		// (DefaultFeatureFlags), which silently re-activated type-implied
		// capabilities the registration/Vendi left OFF on purpose (F037
		// minimal dashboard) the moment any unrelated toggle was PATCHed.
		if req.Config != nil {
			// Load the current tenant to get existing flags
			var tenant models.Tenant
			if err := db.Where("id = ?", tenantID).First(&tenant).Error; err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "negocio no encontrado"})
				return
			}

			flags := tenant.FeatureFlags

			if req.Config.HasTables != nil {
				// Tables toggle grants mesas WITHOUT KDS/Tips (Spec F023 D3);
				// KDS/Tips keep their current value (reel-activated later).
				flags.EnableTables = *req.Config.HasTables
			}
			if req.Config.OffersServices != nil {
				flags.EnableServices = *req.Config.OffersServices
				flags.EnableCustomBilling = *req.Config.OffersServices
			}
			if req.Config.SellsByWeight != nil {
				flags.EnableFractionalUnits = *req.Config.SellsByWeight
			}

			// Spec F042: academias sigue implicando Eventos al AÑADIR el tipo
			// (additive-only), y el request explícito puede togglearlo.
			if req.BusinessTypes != nil {
				for _, bt := range req.BusinessTypes {
					if bt == models.BusinessTypeAcademias {
						flags.EnableEvents = true
						break
					}
				}
			}
			if req.Config.EnableEvents != nil {
				flags.EnableEvents = *req.Config.EnableEvents
			}

			// Spec 105 F3 — "mesero puede cobrar": toggle explícito solamente.
			if req.Config.EnableWaiterCharge != nil {
				flags.EnableWaiterCharge = *req.Config.EnableWaiterCharge
			}

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

			// Spec F031 — módulo de cotizaciones. Toggle simple sin
			// validación adicional; mismo patrón que los toggles previos.
			if req.Config.EnableQuotes != nil {
				updates["enable_quotes"] = *req.Config.EnableQuotes
			}

			// Spec F033 — módulo de difusión de promociones. Toggle simple
			// sin validación adicional; mismo patrón que los toggles previos.
			if req.Config.EnablePromotions != nil {
				updates["enable_promotions"] = *req.Config.EnablePromotions
			}

			// Visibilidad de la sección de Ofertas en el catálogo público.
			if req.Config.HideOffersSection != nil {
				updates["hide_offers_section"] = *req.Config.HideOffersSection
			}

			// Spec F037 — capacidades del reel del Dashboard. Toggles
			// simples sin validación adicional; mismo patrón que los
			// toggles previos.
			if req.Config.EnableMarketingHub != nil {
				updates["enable_marketing_hub"] = *req.Config.EnableMarketingHub
			}
			if req.Config.EnableRecipes != nil {
				updates["enable_recipes"] = *req.Config.EnableRecipes
			}
			if req.Config.EnableSupplies != nil {
				updates["enable_supplies"] = *req.Config.EnableSupplies
			}
			if req.Config.EnableFurnitureJobs != nil {
				updates["enable_furniture_jobs"] = *req.Config.EnableFurnitureJobs
			}
			if req.Config.EnablePurchaseOrders != nil {
				updates["enable_purchase_orders"] = *req.Config.EnablePurchaseOrders
			}

			// Spec 095 — variantes de producto. Toggle simple sin validación
			// adicional; mismo patrón que los toggles previos.
			if req.Config.EnableProductVariants != nil {
				updates["enable_product_variants"] = *req.Config.EnableProductVariants
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
