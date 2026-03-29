package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestParsePagination_Defaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/test", nil)

	p := parsePagination(c)
	assert.Equal(t, 1, p.Page)
	assert.Equal(t, 20, p.PerPage)
}

func TestParsePagination_CustomValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/test?page=3&per_page=50", nil)

	p := parsePagination(c)
	assert.Equal(t, 3, p.Page)
	assert.Equal(t, 50, p.PerPage)
}

func TestParsePagination_ClampsMaxPerPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/test?per_page=500", nil)

	p := parsePagination(c)
	assert.Equal(t, 100, p.PerPage)
}

func TestParsePagination_FixesNegative(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/test?page=-1&per_page=-5", nil)

	p := parsePagination(c)
	assert.Equal(t, 1, p.Page)
	assert.Equal(t, 20, p.PerPage)
}

func TestNewPaginatedResponse(t *testing.T) {
	data := []string{"a", "b"}
	resp := newPaginatedResponse(data, 50, PaginationParams{Page: 2, PerPage: 20})

	assert.Equal(t, int64(50), resp.Total)
	assert.Equal(t, 2, resp.Page)
	assert.Equal(t, 20, resp.PerPage)
	assert.Equal(t, 3, resp.TotalPages)
}
