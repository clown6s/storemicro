package envexpand

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExpand(t *testing.T) {
	os.Setenv("TEST_EXPAND_VAR", "expanded_value")
	defer os.Unsetenv("TEST_EXPAND_VAR")

	result := ExpandEnv("${TEST_EXPAND_VAR}")
	assert.Equal(t, "expanded_value", result)
}

func TestExpand_DefaultValue(t *testing.T) {
	os.Unsetenv("UNSET_VAR_FOR_DEFAULT")

	result := ExpandEnv("${UNSET_VAR_FOR_DEFAULT:default_val}")
	assert.Equal(t, "default_val", result)
}

func TestExpand_NoVariable(t *testing.T) {
	result := ExpandEnv("plain string")
	assert.Equal(t, "plain string", result)
}

func TestExpand_EmptyDefault(t *testing.T) {
	os.Unsetenv("UNSET_VAR_EMPTY_DEF")

	result := ExpandEnv("${UNSET_VAR_EMPTY_DEF:}")
	assert.Equal(t, "", result)
}

func TestExpand_MultipleVariables(t *testing.T) {
	os.Setenv("TEST_HOST", "db.local")
	os.Setenv("TEST_PORT", "3306")
	defer func() {
		os.Unsetenv("TEST_HOST")
		os.Unsetenv("TEST_PORT")
	}()

	result := ExpandEnv("host=${TEST_HOST:localhost}:${TEST_PORT:5432}")
	assert.Equal(t, "host=db.local:3306", result)
}

func TestExpand_BraceSyntax(t *testing.T) {
	os.Setenv("BRACE_VAR", "brace_val")
	defer os.Unsetenv("BRACE_VAR")

	result := ExpandEnv("${BRACE_VAR}")
	assert.Equal(t, "brace_val", result)
}

func TestExpand_BareSyntax(t *testing.T) {
	os.Setenv("BARE_VAR", "bare_val")
	defer os.Unsetenv("BARE_VAR")

	result := ExpandEnv("$BARE_VAR")
	assert.Equal(t, "bare_val", result)
}

func TestExpand_UnsetVarPreserved(t *testing.T) {
	os.Unsetenv("TOTALLY_UNSET_VAR_xyz123")

	result := ExpandEnv("${TOTALLY_UNSET_VAR_xyz123}")
	assert.Equal(t, "${TOTALLY_UNSET_VAR_xyz123}", result)

	result2 := ExpandEnv("$TOTALLY_UNSET_VAR_xyz123")
	assert.Equal(t, "$TOTALLY_UNSET_VAR_xyz123", result2)
}

func TestExpand_EnvOverridesDefault(t *testing.T) {
	os.Setenv("TEST_OVERRIDE", "real_value")
	defer os.Unsetenv("TEST_OVERRIDE")

	result := ExpandEnv("${TEST_OVERRIDE:fallback}")
	assert.Equal(t, "real_value", result)
}

func TestExpand_PasswordWithDollar(t *testing.T) {
	// Test that passwords containing $ are not double-expanded
	result := ExpandEnv("p@ss$word")
	assert.Equal(t, "p@ss$word", result)
}
