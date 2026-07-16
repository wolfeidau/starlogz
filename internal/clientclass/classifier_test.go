package clientclass

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFromMCPClassifiesVerifiedCodexName(t *testing.T) {
	c := FromMCP("  CODEX-MCP-CLIENT ", "v144.4.1")

	require.Equal(t, ProductCodex, c.Product)
	require.Equal(t, SourceMCPInitialize, c.Source)
	require.Equal(t, ConfidenceDeclared, c.Confidence)
	require.True(t, c.HasMajor)
	require.Equal(t, 144, c.Major)
	require.True(t, Valid(c))
}

func TestFromOAuthClassifiesVerifiedCodexNames(t *testing.T) {
	for _, name := range []string{"Codex", "codex-mcp-client"} {
		t.Run(name, func(t *testing.T) {
			c := FromOAuth(name)
			require.Equal(t, ProductCodex, c.Product)
			require.Equal(t, SourceOAuthRegistration, c.Source)
			require.Equal(t, ConfidenceDeclared, c.Confidence)
			require.False(t, c.HasMajor)
		})
	}
}

func TestFromFirstPartyUsesStableClientID(t *testing.T) {
	require.Equal(t, ProductStarlogzUI, FromFirstParty("starlogz-ui").Product)
	require.Equal(t, ConfidenceFirstParty, FromFirstParty("starlogz-ui").Confidence)
	require.Equal(t, Unknown(), FromFirstParty("Starlogz UI"))
}

func TestUnknownInputsNeverEscape(t *testing.T) {
	secret := "private-client/secret-value"
	for _, c := range []Classification{
		FromMCP(secret, secret),
		FromOAuth(secret),
		FromUserAgent(secret),
	} {
		require.Equal(t, Unknown(), c)
		require.NotContains(t, c.Product, secret)
		require.NotContains(t, c.Source, secret)
		require.NotContains(t, c.Confidence, secret)
	}
}

func TestMajorVersionBounds(t *testing.T) {
	tests := map[string]struct {
		major    int
		hasMajor bool
	}{
		"0.1.0":    {major: 0, hasMajor: true},
		"999.0":    {major: 999, hasMajor: true},
		"1000.0":   {},
		"unknown":  {},
		"":         {},
		"v12-beta": {major: 12, hasMajor: true},
	}
	for version, expected := range tests {
		t.Run(version, func(t *testing.T) {
			c := FromMCP("codex-mcp-client", version)
			require.Equal(t, expected.hasMajor, c.HasMajor)
			require.Equal(t, expected.major, c.Major)
		})
	}
}

func TestNormalizeRejectsInvalidCombinations(t *testing.T) {
	tests := []Classification{
		{Product: "private", Source: SourceMCPInitialize, Confidence: ConfidenceDeclared},
		{Product: ProductCodex, Source: SourceUnknown, Confidence: ConfidenceDeclared},
		{Product: ProductCodex, Source: SourceMCPInitialize, Confidence: ConfidenceSignature},
		{Product: ProductOther, Source: SourceUserAgent, Confidence: ConfidenceUnknown},
		{Product: ProductCodex, Major: 1000, HasMajor: true, Source: SourceMCPInitialize, Confidence: ConfidenceDeclared},
	}
	for _, c := range tests {
		require.Equal(t, Unknown(), Normalize(c))
	}
}

func TestFromTokenInfoValidatesBoundedValues(t *testing.T) {
	c := FromTokenInfo(map[string]any{
		TokenInfoProductKey: ProductCodex, TokenInfoMajorKey: 144,
		TokenInfoConfidenceKey: ConfidenceDeclared,
	})
	require.Equal(t, ProductCodex, c.Product)
	require.Equal(t, 144, c.Major)
	require.True(t, c.HasMajor)

	require.Equal(t, Unknown(), FromTokenInfo(map[string]any{
		TokenInfoProductKey: "private", TokenInfoConfidenceKey: ConfidenceDeclared,
	}))
}
