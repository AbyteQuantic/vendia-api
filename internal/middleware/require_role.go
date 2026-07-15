// Spec: specs/105-hito-restaurante-comandas/spec.md — F3.
//
// RequireBackOffice protege las superficies de DINERO y CONFIGURACIÓN
// (reportes, perfil del negocio, gestión de empleados) de los roles de
// piso nuevos (waiter/chef/courier).
//
// RETRO-COMPAT (riesgo explícito del concilio): un token SIN claim de rol
// (empleados legacy, cuentas mono-tenant viejas) conserva ACCESO TOTAL —
// bloquearlo rompería tenants desplegados. Solo se niega a los roles que
// NACEN con esta fase; owner/admin/cashier quedan intactos.
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func RequireBackOffice() gin.HandlerFunc {
	return func(c *gin.Context) {
		role := strings.ToLower(GetRole(c))
		switch role {
		case "waiter", "chef", "courier":
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "su rol no tiene acceso a esta sección",
			})
			return
		}
		c.Next()
	}
}
