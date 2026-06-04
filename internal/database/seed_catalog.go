// Spec: specs/041-catalogo-dinamico-modulos-tipos/spec.md
//
// Seed de PARIDAD del catálogo dinámico (F041). Carga como datos iniciales
// el catálogo que hoy está hardcodeado (módulos del dashboard + 9 tipos de
// negocio + relaciones implícita/sugerida) para que, al activar la feature,
// ninguna tienda pierda un módulo que veía antes (AC-11).
//
// Idempotente: si ya hay módulos en la tabla, es no-op. Pensado para correr
// en el bootstrap de Go (no SQL crudo — Art. X).

package database

import (
	"gorm.io/gorm"

	"vendia-backend/internal/models"
)

// catalogModuleSeed describe un módulo a sembrar. capabilityKey vacío = core.
type catalogModuleSeed struct {
	key, name, desc, iconKey, color, category, screenKey, capabilityKey string
}

// strp devuelve *string para un valor no vacío, o nil si está vacío.
func strp(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

// SeedBusinessCatalog carga el catálogo inicial si está vacío. Devuelve el
// número de módulos creados (0 si fue no-op).
func SeedBusinessCatalog(db *gorm.DB) (int, error) {
	var existing int64
	if err := db.Model(&models.BusinessModule{}).Count(&existing).Error; err != nil {
		return 0, err
	}
	if existing > 0 {
		return 0, nil // ya sembrado — no-op
	}

	// ── Tipos de negocio (9 actuales) ──────────────────────────────────
	types := []models.BusinessTypeCatalog{
		{Value: models.BusinessTypeTiendaBarrio, Label: "Tienda de Barrio", IconKey: "store_rounded", Active: true, SortOrder: 0},
		{Value: models.BusinessTypeMinimercado, Label: "Minimercado", IconKey: "local_grocery_store_rounded", Active: true, SortOrder: 1},
		{Value: models.BusinessTypeDepositoConstruccion, Label: "Depósito / Ferretería", IconKey: "inventory_2_rounded", Active: true, SortOrder: 2},
		{Value: models.BusinessTypeRestaurante, Label: "Restaurante", IconKey: "restaurant_rounded", Active: true, SortOrder: 3},
		{Value: models.BusinessTypeComidasRapidas, Label: "Comidas Rápidas", IconKey: "fastfood_rounded", Active: true, SortOrder: 4},
		{Value: models.BusinessTypeBar, Label: "Bar / Discoteca", IconKey: "local_bar_rounded", Active: true, SortOrder: 5},
		{Value: models.BusinessTypeManufactura, Label: "Manufactura", IconKey: "precision_manufacturing_rounded", Active: true, SortOrder: 6},
		{Value: models.BusinessTypeReparacionMuebles, Label: "Reparación / Servicios", IconKey: "build_rounded", Active: true, SortOrder: 7},
		{Value: models.BusinessTypeEmprendimientoGen, Label: "Emprendimiento", IconKey: "rocket_launch_rounded", Active: true, SortOrder: 8},
	}

	// ── Módulos (espejo de dashboard_modules.dart) ─────────────────────
	seeds := []catalogModuleSeed{
		{"registrar_venta", "Registrar venta", "Cobre rápido y registre el pago", "point_of_sale_rounded", "#1A2FA0", models.CategoryVender, "pos", ""},
		{"historial", "Historial de ventas", "Vea todas las ventas registradas", "receipt_long_rounded", "#3B82F6", models.CategoryVender, "sales_history", ""},
		{"analisis_ganancias", "Análisis de Ganancias", "Utilidad, márgenes e ingresos por método", "bar_chart_rounded", "#059669", models.CategoryVender, "financial_dashboard", ""},
		{"cotizaciones", "Cotizaciones", "Arme y envíe propuestas de precio", "description_outlined", "#1A2FA0", models.CategoryVender, "quotes", "enable_quotes"},
		{"productos", "Productos", "Agregue mercancía, edite precios y stock", "inventory_2_rounded", "#6366F1", models.CategoryInventario, "add_merchandise", ""},
		{"reporte_inventario", "Reporte de Inventario", "Kardex, entradas, salidas y stock", "assessment_rounded", "#059669", models.CategoryInventario, "inventory_report", ""},
		{"proveedores", "Mis Proveedores", "Pedidos por WhatsApp, llamada o SMS", "local_shipping_rounded", "#764BA2", models.CategoryInventario, "suppliers", ""},
		{"insumos", "Mis Insumos", "Materia prima: stock, mínimos y costo", "kitchen_rounded", "#D97706", models.CategoryInventario, "ingredients", "enable_supplies"},
		{"recetas", "Recetas y Platos", "Arme un plato y vea su costo y ganancia", "restaurant_menu_rounded", "#EE5A24", models.CategoryInventario, "recipes", "enable_recipes"},
		{"ordenes_compra", "Órdenes de Compra", "Pida a proveedores y reciba el stock", "shopping_cart_rounded", "#0D9668", models.CategoryInventario, "purchase_orders", "enable_purchase_orders"},
		{"trabajos_muebles", "Trabajos de Muebles", "Cotice, fabrique y repare por encargo", "handyman_rounded", "#1A2FA0", models.CategoryInventario, "work_orders", "enable_furniture_jobs"},
		{"mis_clientes", "Mis Clientes", "Quién le compra: historial y total gastado", "people_outline", "#1A2FA0", models.CategoryClientes, "customers", "enable_customer_management"},
		{"promociones", "Promociones", "Avísele a sus clientes cuando tenga ofertas", "campaign_rounded", "#D97706", models.CategoryClientes, "promotions", "enable_promotions"},
		{"marketing_hub", "Marketing y Combos", "Combos, banners con IA y catálogo en línea", "auto_awesome_rounded", "#7C3AED", models.CategoryMiNegocio, "promo_management", "enable_marketing_hub"},
		{"configuracion", "Ajustes de mi Negocio", "Perfil, capacidades, empleados y dispositivos", "settings_rounded", "#1E3A8A", models.CategoryMiNegocio, "admin_hub", ""},
	}

	// Relaciones "sugerido" (espejo de defaultCapabilitiesForType para los
	// módulos-card). Las capacidades-toggle (mesas/servicios/granel/precios)
	// no son tarjetas del dashboard y viven en FeatureFlags, no aquí.
	suggested := map[string][]string{
		"cotizaciones": {models.BusinessTypeDepositoConstruccion, models.BusinessTypeManufactura, models.BusinessTypeReparacionMuebles},
		"mis_clientes": {models.BusinessTypeDepositoConstruccion, models.BusinessTypeManufactura, models.BusinessTypeReparacionMuebles, models.BusinessTypeEmprendimientoGen},
	}

	created := 0
	err := db.Transaction(func(tx *gorm.DB) error {
		for i := range types {
			if err := tx.Create(&types[i]).Error; err != nil {
				return err
			}
		}

		keyToID := make(map[string]string, len(seeds))
		for i, s := range seeds {
			m := models.BusinessModule{
				Key:             s.key,
				Name:            s.name,
				Description:     s.desc,
				IconKey:         s.iconKey,
				Color:           s.color,
				Category:        s.category,
				RenderType:      models.RenderNative,
				NativeScreenKey: strp(s.screenKey),
				CapabilityKey:   strp(s.capabilityKey),
				Active:          true,
				SortOrder:       i,
				CreatedBy:       "seed",
			}
			if err := tx.Create(&m).Error; err != nil {
				return err
			}
			keyToID[s.key] = m.ID
			created++
		}

		for moduleKey, typeValues := range suggested {
			moduleID := keyToID[moduleKey]
			for _, tv := range typeValues {
				rel := models.ModuleTypeRelation{
					ModuleID:          moduleID,
					BusinessTypeValue: tv,
					RelationLevel:     models.RelationSuggested,
				}
				if err := tx.Create(&rel).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}
