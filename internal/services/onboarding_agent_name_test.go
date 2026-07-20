// Spec: specs/106-onboarding-conversacional-agente/spec.md (Adenda A)
package services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAgentFirstName(t *testing.T) {
	assert.Equal(t, "Carmen", AgentFirstName("carmen lópez"))
	assert.Equal(t, "Brayan", AgentFirstName("  BRAYAN MURCIA  "))
	assert.Equal(t, "", AgentFirstName("   "))
	assert.Equal(t, "", AgentFirstName(""))
}

func TestAgentGreetingUsesOwnerName(t *testing.T) {
	say := strings.Join(AgentGreeting("carmen lópez"), " ")
	assert.Contains(t, say, "Carmen")
	assert.NotContains(t, say, "don")

	anon := strings.Join(AgentGreeting(""), " ")
	assert.Contains(t, anon, "Soy <b>Vendi</b>")
	assert.NotContains(t, anon, ", !")
}
