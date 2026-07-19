package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"vendia-backend/internal/models"
	"vendia-backend/internal/services"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type TenantRegisterRequest struct {
	Owner OwnerInput `json:"owner" binding:"required"`
	// Spec 106 — registro mínimo: el flujo corto de Vendi solo manda
	// credenciales; business/config son opcionales (la conversación los
	// define después). Las apps viejas siguen mandando el payload completo
	// y se comportan igual (Art. X).
	Business  *BusinessInput  `json:"business"`
	Config    *ConfigInput    `json:"config"`
	Employees []EmployeeInput `json:"employees"`
	// Spec 098 — aceptación de Términos (incluye uso colaborativo de imágenes).
	// Fail-closed: sin aceptación no se crea la cuenta. La UI lo captura con un
	// checkbox obligatorio.
	AcceptTerms bool `json:"accept_terms"`
	// Spec 106 (FR-13) — el registro mostró el aviso de que la conversación
	// con el asistente se guarda para mejorar el servicio. Informativo.
	DataNoticeAccepted bool `json:"data_notice_accepted"`
}

type OwnerInput struct {
	Name  string `json:"name"     binding:"required"`
	Phone string `json:"phone"    binding:"required,min=7,max=15"`
	// La UI Flutter (login + onboarding step_owner) aplica
	// LengthLimitingTextInputFormatter(8) en el campo de clave/PIN. El
	// backend tiene que cumplir el mismo contrato — sin max, un cliente
	// distinto (curl, móvil con bug, integración futura) puede crear
	// cuentas con >8 chars que después no entren por la UI normal. El
	// max=8 alinea ambos lados y previene ese estado inválido.
	Password string `json:"password" binding:"required,min=4,max=8"`
}

type BusinessInput struct {
	// Name es opcional desde Spec 106 (registro mínimo): vacío → placeholder
	// "Mi negocio"; Vendi pregunta el nombre real en la conversación.
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Types       []string `json:"types"`
	RazonSocial string   `json:"razon_social"`
	NIT         string   `json:"nit"`
	Address     string   `json:"address"`
	// LogoURL — set when the merchant generated/uploaded a logo
	// during the new step 5 (pre-register). Persisted on the tenant
	// at creation time so the merchant lands on the dashboard with
	// the brand mark already in place.
	LogoURL string `json:"logo_url"`
}

type ConfigInput struct {
	SaleTypes       []string `json:"sale_types"       binding:"required,min=1"`
	HasShowcases    bool     `json:"has_showcases"`
	HasTables       bool     `json:"has_tables"`
	OffersServices  bool     `json:"offers_services"`
	SellsByWeight   bool     `json:"sells_by_weight"`
}

type EmployeeInput struct {
	Name  string              `json:"name"     binding:"required"`
	Phone string              `json:"phone"`
	Role  models.EmployeeRole `json:"role"     binding:"required"`
	// Mismo cap que el dueño — la UI de empleados también limita a 8.
	Password string `json:"password" binding:"required,min=4,max=8"`
}

func TenantRegister(db *gorm.DB, jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req TenantRegisterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Spec 098 — fail-closed: no se crea la cuenta sin aceptar los términos
		// (incluida la cláusula de uso colaborativo de imágenes).
		if !req.AcceptTerms {
			c.JSON(http.StatusBadRequest, gin.H{"error": "debe aceptar los Términos y Servicios para crear la cuenta"})
			return
		}

		// Check both users and tenants tables for existing phone
		var existingUser models.User
		userExists := db.Where("phone = ?", req.Owner.Phone).First(&existingUser).Error == nil
		var existingTenant models.Tenant
		tenantExists := db.Where("phone = ?", req.Owner.Phone).First(&existingTenant).Error == nil

		if userExists || tenantExists {
			c.JSON(http.StatusConflict, gin.H{"error": "ese número ya está registrado"})
			return
		}

		// Spec 106 — normalización del registro mínimo: bloques ausentes caen
		// a defaults sensatos; el payload completo de apps viejas pasa intacto.
		business := BusinessInput{}
		if req.Business != nil {
			business = *req.Business
		}
		businessName := strings.TrimSpace(business.Name)
		if businessName == "" {
			businessName = "Mi negocio"
		}
		cfg := ConfigInput{}
		if req.Config != nil {
			cfg = *req.Config
		}
		if len(cfg.SaleTypes) == 0 {
			cfg.SaleTypes = []string{"products"}
		}

		businessTypes, validationErr := validateBusinessTypes(resolveBusinessTypes(business))
		if validationErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": validationErr.Error()})
			return
		}

		ownerHash, err := bcrypt.GenerateFromPassword([]byte(req.Owner.Password), 12)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al procesar contraseña"})
			return
		}

		// Spec F037 §4.1 — defaults mínimos. F036 used to pre-activate
		// capabilities typical of the chosen business type; F037 reverts
		// that so every type lands on the same minimal Dashboard and the
		// merchant discovers extras through the capabilities reel. Two
		// pieces work together here:
		//
		//   - DefaultCapabilitiesForTypes returns Capabilities{} for every
		//     type (kept for forward compatibility).
		//   - DefaultFeatureFlags is called with []string{} instead of
		//     businessTypes so the food/services/deposito branches that
		//     auto-activate EnableTables/KDS/Tips/Services/FractionalUnits
		//     by type are bypassed. Only the form toggles a tendero
		//     explicitly ticked propagate.
		//
		// Net result: all enable_* columns land FALSE at registration
		// (AC-01), except the ones the registration form itself opted into.
		caps := services.DefaultCapabilitiesForTypes(businessTypes) // F037: always Capabilities{}

		flags := models.DefaultFeatureFlags(nil, models.CapabilityToggles{
			Tables:          cfg.HasTables || caps.Tables,
			Services:        cfg.OffersServices || caps.Services,
			FractionalUnits: cfg.SellsByWeight,
		})

		// Spec 075 — ser proveedor B2B SÍ se deriva del tipo al registrarse: es
		// la identidad del negocio (no una capacidad opcional como Mesas/KDS que
		// se dejan en false por AC-01). Sin esto, un negocio que se registra como
		// "Proveedor mayorista/agrícola" no vería su Panel de proveedor.
		for _, t := range businessTypes {
			if t == models.BusinessTypeProveedorAgricola ||
				t == models.BusinessTypeProveedorMayorista {
				flags.EnableSupplierMode = true
				break
			}
		}

		var tenant models.Tenant
		var user models.User

		txErr := db.Transaction(func(tx *gorm.DB) error {
			// 1. Create User (global identity)
			user = models.User{
				Phone:        req.Owner.Phone,
				Name:         req.Owner.Name,
				PasswordHash: string(ownerHash),
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}

			// 2. Create Tenant (business) — dual-write phone/password for legacy compat
			tenant = models.Tenant{
				OwnerName:     req.Owner.Name,
				Phone:         req.Owner.Phone,
				PasswordHash:  string(ownerHash),
				BusinessName:  businessName,
				BusinessTypes: businessTypes,
				FeatureFlags:  flags,
				RazonSocial:   business.RazonSocial,
				NIT:           business.NIT,
				Address:       business.Address,
				SaleTypes:     cfg.SaleTypes,
				HasShowcases:  cfg.HasShowcases,
				HasTables:     cfg.HasTables || flags.EnableTables,
				LogoURL:       business.LogoURL,
				// Spec F036 §4.2 — standalone capability columns
				// pre-activated from the business type. OR'd with no
				// form input (the register form doesn't collect these),
				// so they come straight from DefaultCapabilitiesForTypes.
				// They remain freely togglable later (Spec F036 §4.3).
				EnablePriceTiers:         caps.PriceTiers,
				EnableCustomerManagement: caps.CustomerMgmt,
				EnableQuotes:             caps.Quotes,
				// OnboardingCompleted is left as its zero value (false):
				// every new tenant sees the onboarding wizard once.
				// Spec 098 — aceptación de términos capturada en el registro.
				TermsAcceptedVersion: models.CatalogTermsVersion,
				TermsAcceptedAt:      func() *time.Time { t := time.Now(); return &t }(),
			}
			// Spec 106 (FR-13) — aviso de datos del onboarding conversacional.
			if req.DataNoticeAccepted {
				now := time.Now()
				tenant.DataNoticeAcceptedAt = &now
			}
			if err := tx.Create(&tenant).Error; err != nil {
				return err
			}

			// 2.b Seed the only payment method that's active by
			//     default for new tenants. The merchant configures
			//     Nequi / Daviplata / Tarjeta / Fiar later from the
			//     admin screen by toggling is_active and uploading
			//     their QR. Without this seed, the public catalog
			//     and the POS payment chips fall back to hardcoded
			//     UI defaults — masking the fact that the tenant
			//     literally has zero rows in payment_methods.
			defaultMethod := models.TenantPaymentMethod{
				TenantID: tenant.ID,
				Name:     "Efectivo",
				Provider: "cash",
				IsActive: true,
			}
			if err := tx.Create(&defaultMethod).Error; err != nil {
				return fmt.Errorf("failed to seed default payment method: %w", err)
			}

			// 3. Create default Branch
			branch := models.Branch{
				TenantID:  tenant.ID,
				Name:      "Principal",
				Address:   business.Address,
				IsActive:  true,
				IsDefault: true,
			}
			if err := tx.Create(&branch).Error; err != nil {
				return err
			}

			// 4. Create UserWorkspace (owner link)
			ws := models.UserWorkspace{
				UserID:    user.ID,
				TenantID:  tenant.ID,
				BranchID:  &branch.ID,
				Role:      models.RoleOwner,
				IsDefault: true,
			}
			if err := tx.Create(&ws).Error; err != nil {
				return err
			}

			// 4.b Create the TenantSubscription — Feature 008 (FR-02 /
			//     AC-01). Historically a DB trigger was supposed to do
			//     this, but Render only runs GORM AutoMigrate (never the
			//     goose .sql), so the trigger never fired and every new
			//     tenant landed with NO subscription row → the soft
			//     paywall 403'd them on first premium request. Creating
			//     the row HERE, inside the same transaction as the
			//     tenant, makes the trial real and atomic: if it fails,
			//     the whole registration rolls back rather than leaving
			//     a tenant stranded.
			trialEnds := time.Now().Add(models.TrialDays * 24 * time.Hour)
			sub := models.TenantSubscription{
				TenantID:    tenant.ID,
				Status:      models.SubscriptionStatusTrial,
				Plan:        models.SubscriptionPlanFree,
				TrialEndsAt: &trialEnds,
			}
			if err := tx.Create(&sub).Error; err != nil {
				return fmt.Errorf("failed to create trial subscription: %w", err)
			}

			// 5. Create Employee(s) — each one is scoped to the
			//    "Sede Principal" we just created. Multi-branch
			//    tenants (PRO) reassign employees later through the
			//    /api/v1/store/employees PATCH endpoint.
			branchIDPtr := &branch.ID
			if len(req.Employees) == 0 {
				defaultCashier := models.Employee{
					TenantID:     tenant.ID,
					BranchID:     branchIDPtr,
					Name:         req.Owner.Name,
					Phone:        req.Owner.Phone,
					Role:         models.RoleCashier,
					PasswordHash: string(ownerHash),
					IsOwner:      true,
					IsActive:     true,
				}
				return tx.Create(&defaultCashier).Error
			}

			for _, emp := range req.Employees {
				empHash, err := bcrypt.GenerateFromPassword([]byte(emp.Password), 12)
				if err != nil {
					return err
				}
				employee := models.Employee{
					TenantID:     tenant.ID,
					BranchID:     branchIDPtr,
					Name:         emp.Name,
					Phone:        emp.Phone,
					Role:         emp.Role,
					PasswordHash: string(empHash),
					IsOwner:      false,
					IsActive:     true,
				}
				if err := tx.Create(&employee).Error; err != nil {
					return err
				}
			}

			return nil
		})

		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar el negocio"})
			return
		}

		// Generate workspace-aware token
		resp, err := createWorkspaceTokenPair(db, user, tenant.ID, "", tenant.BusinessName, string(models.RoleOwner), jwtSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "error al generar tokens"})
			return
		}

		c.JSON(http.StatusCreated, resp)
	}
}

// resolveBusinessTypes supports both legacy single "type" and new "types" array.
func resolveBusinessTypes(b BusinessInput) []string {
	if len(b.Types) > 0 {
		return b.Types
	}
	if b.Type != "" {
		return []string{b.Type}
	}
	return []string{}
}

// validateBusinessTypes remaps legacy values to the unified taxonomy and
// rejects anything outside the whitelist. The DB CHECK would catch the
// bad value too but we want a Spanish-language error at the app layer.
// Legacy → unified mapping mirrors migration 020's UPDATE statement so
// both the startup backfill and a fresh register land on the same values.
func validateBusinessTypes(raw []string) ([]string, error) {
	legacyMap := map[string]string{
		"muebles":    models.BusinessTypeReparacionMuebles,
		"miscelanea": models.BusinessTypeEmprendimientoGen,
		"reparacion": models.BusinessTypeReparacionMuebles,
	}

	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if mapped, ok := legacyMap[t]; ok {
			t = mapped
		}
		if _, ok := models.ValidBusinessTypes[t]; !ok {
			return nil, fmt.Errorf("tipo de negocio no válido: %q", t)
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}
