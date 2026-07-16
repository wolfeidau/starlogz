package clientclass

import (
	"strconv"
	"strings"
)

const codexMCPClientName = "codex-mcp-client"

const (
	ProductStarlogzUI   = "starlogz_ui"
	ProductCodex        = "codex"
	ProductClaudeCode   = "claude_code"
	ProductCursor       = "cursor"
	ProductVSCode       = "vscode"
	ProductMCPInspector = "mcp_inspector"
	ProductOther        = "other"
)

const (
	SourceFirstParty        = "first_party"
	SourceMCPInitialize     = "mcp_initialize"
	SourceOAuthRegistration = "oauth_registration"
	SourceUserAgent         = "user_agent"
	SourceUnknown           = "unknown"
)

const (
	ConfidenceFirstParty = "first_party"
	ConfidenceDeclared   = "declared"
	ConfidenceSignature  = "signature"
	ConfidenceUnknown    = "unknown"
)

const (
	TokenInfoProductKey    = "client_product"
	TokenInfoMajorKey      = "client_product_major"
	TokenInfoConfidenceKey = "client_identity_confidence"
)

type Classification struct {
	Product    string
	Major      int
	HasMajor   bool
	Source     string
	Confidence string
}

func Unknown() Classification {
	return Classification{
		Product: ProductOther, Source: SourceUnknown, Confidence: ConfidenceUnknown,
	}
}

func FromFirstParty(clientID string) Classification {
	if strings.TrimSpace(clientID) != "starlogz-ui" {
		return Unknown()
	}
	return Classification{
		Product: ProductStarlogzUI, Source: SourceFirstParty, Confidence: ConfidenceFirstParty,
	}
}

func FromMCP(name, version string) Classification {
	switch normalize(name) {
	case codexMCPClientName:
		return declared(ProductCodex, SourceMCPInitialize, version)
	default:
		return Unknown()
	}
}

func FromOAuth(clientName string) Classification {
	switch normalize(clientName) {
	case "codex", codexMCPClientName:
		return Classification{
			Product: ProductCodex, Source: SourceOAuthRegistration, Confidence: ConfidenceDeclared,
		}
	default:
		return Unknown()
	}
}

// FromUserAgent intentionally starts with no product rules. Add only narrowly
// anchored signatures verified through controlled clients or authoritative sources.
func FromUserAgent(string) Classification {
	return Unknown()
}

func FromTokenInfo(extra map[string]any) Classification {
	if len(extra) == 0 {
		return Unknown()
	}
	product, ok := extra[TokenInfoProductKey].(string)
	if !ok {
		return Unknown()
	}
	confidence, ok := extra[TokenInfoConfidenceKey].(string)
	if !ok {
		return Unknown()
	}
	c := Classification{
		Product: product, Source: SourceOAuthRegistration, Confidence: confidence,
	}
	if product == ProductStarlogzUI && confidence == ConfidenceFirstParty {
		c.Source = SourceFirstParty
	}
	if major, ok := extra[TokenInfoMajorKey].(int); ok {
		c.Major = major
		c.HasMajor = true
	}
	return Normalize(c)
}

func Normalize(c Classification) Classification {
	if !Valid(c) {
		return Unknown()
	}
	return c
}

func Valid(c Classification) bool {
	if !validProduct(c.Product) || !validSource(c.Source) || !validConfidence(c.Confidence) {
		return false
	}
	if c.HasMajor && (c.Major < 0 || c.Major > 999) {
		return false
	}
	if c.Product == ProductOther {
		return c.Source == SourceUnknown && c.Confidence == ConfidenceUnknown && !c.HasMajor
	}
	switch c.Confidence {
	case ConfidenceFirstParty:
		return c.Source == SourceFirstParty && c.Product == ProductStarlogzUI
	case ConfidenceDeclared:
		return c.Source == SourceMCPInitialize || c.Source == SourceOAuthRegistration
	case ConfidenceSignature:
		return c.Source == SourceUserAgent
	default:
		return false
	}
}

func Recognized(c Classification) bool {
	return Valid(c) && c.Product != ProductOther
}

func declared(product, source, version string) Classification {
	c := Classification{Product: product, Source: source, Confidence: ConfidenceDeclared}
	if major, ok := parseMajor(version); ok {
		c.Major = major
		c.HasMajor = true
	}
	return c
}

func parseMajor(version string) (int, bool) {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	end := 0
	for end < len(version) && version[end] >= '0' && version[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	major, err := strconv.Atoi(version[:end])
	if err != nil || major > 999 {
		return 0, false
	}
	return major, true
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validProduct(value string) bool {
	switch value {
	case ProductStarlogzUI, ProductCodex, ProductClaudeCode, ProductCursor,
		ProductVSCode, ProductMCPInspector, ProductOther:
		return true
	default:
		return false
	}
}

func validSource(value string) bool {
	switch value {
	case SourceFirstParty, SourceMCPInitialize, SourceOAuthRegistration, SourceUserAgent, SourceUnknown:
		return true
	default:
		return false
	}
}

func validConfidence(value string) bool {
	switch value {
	case ConfidenceFirstParty, ConfidenceDeclared, ConfidenceSignature, ConfidenceUnknown:
		return true
	default:
		return false
	}
}
