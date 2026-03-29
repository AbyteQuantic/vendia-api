package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmployeeRole_Constants(t *testing.T) {
	assert.Equal(t, EmployeeRole("admin"), RoleAdmin)
	assert.Equal(t, EmployeeRole("cashier"), RoleCashier)
}

func TestEmployee_Defaults(t *testing.T) {
	emp := Employee{
		Name: "María",
		Role: RoleCashier,
	}
	assert.Equal(t, "María", emp.Name)
	assert.Equal(t, RoleCashier, emp.Role)
	assert.False(t, emp.IsOwner)
	assert.False(t, emp.IsActive) // zero value, set by DB default
}
