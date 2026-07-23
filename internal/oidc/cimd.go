package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	storepkg "github.com/wolfeidau/starlogz/internal/store"
)

const (
	maxCIMDClientIDLen    = 2048
	maxCIMDResponseBytes  = 5 << 10
	defaultCIMDTimeoutSec = 3
)

var (
	ErrCIMDIneligible      = errors.New("cimd client_id ineligible")
	ErrCIMDInvalidMetadata = errors.New("cimd metadata invalid")
)

var deniedIPPrefixes = mustParsePrefixes(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"255.255.255.255/32",
	"::/128",
	"::1/128",
	"::ffff:0:0/96",
	"64:ff9b::/96",
	"100::/64",
	"2001:db8::/32",
	"2001:2::/48",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
)

type ClientIDMetadataResolver interface {
	Resolve(ctx context.Context, clientID string) (*resolvedOAuthClient, error)
}

type cimdDocument struct {
	ClientID string `json:"client_id"`
	oauthex.ClientRegistrationMetadata
}

type httpClientIDMetadataResolver struct {
	client   *http.Client
	lookupIP func(ctx context.Context, network, host string) ([]net.IP, error)
}

func newHTTPClientIDMetadataResolver() *httpClientIDMetadataResolver {
	lookupIP := net.DefaultResolver.LookupIP
	return &httpClientIDMetadataResolver{
		client:   newCIMDHTTPClient(lookupIP),
		lookupIP: lookupIP,
	}
}

func (r *httpClientIDMetadataResolver) Resolve(ctx context.Context, clientID string) (*resolvedOAuthClient, error) {
	u, err := parseEligibleCIMDClientID(clientID)
	if err != nil {
		return nil, err
	}

	if _, err := resolvePublicIPs(ctx, r.lookupIP, u.Hostname()); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, fmt.Errorf("build CIMD request: %w", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch CIMD metadata: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: unexpected status %d", ErrCIMDInvalidMetadata, resp.StatusCode)
	}
	if !isJSONContentType(resp.Header.Get("Content-Type")) {
		return nil, fmt.Errorf("%w: response content-type must be JSON", ErrCIMDInvalidMetadata)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCIMDResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read CIMD metadata: %w", err)
	}
	if len(body) > maxCIMDResponseBytes {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrCIMDInvalidMetadata, maxCIMDResponseBytes)
	}

	var doc cimdDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON", ErrCIMDInvalidMetadata)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%w: response must contain one JSON object", ErrCIMDInvalidMetadata)
	}

	normalizeCIMDMetadata(&doc.ClientRegistrationMetadata)
	if err := validateCIMDDocument(clientID, &doc); err != nil {
		return nil, err
	}

	return &resolvedOAuthClient{
		ClientID:       clientID,
		ClientName:     doc.ClientName,
		ClientKind:     storepkg.OAuthClientKindCIMD,
		RedirectURIs:   doc.RedirectURIs,
		Scope:          doc.Scope,
		RefreshAllowed: slices.Contains(doc.GrantTypes, oauthGrantRefreshToken),
	}, nil
}

func parseEligibleCIMDClientID(clientID string) (*url.URL, error) {
	if clientID == "" || len(clientID) > maxCIMDClientIDLen {
		return nil, ErrCIMDIneligible
	}
	u, err := url.Parse(clientID)
	if err != nil {
		return nil, ErrCIMDIneligible
	}
	if !u.IsAbs() || u.Scheme != redirectSchemeHTTPS || u.Hostname() == "" || u.User != nil {
		return nil, ErrCIMDIneligible
	}
	if u.Fragment != "" || u.RawQuery != "" {
		return nil, ErrCIMDIneligible
	}
	if u.Port() != "" {
		return nil, ErrCIMDIneligible
	}
	if net.ParseIP(u.Hostname()) != nil {
		return nil, ErrCIMDIneligible
	}
	if u.EscapedPath() == "" || !strings.HasPrefix(u.EscapedPath(), "/") {
		return nil, ErrCIMDIneligible
	}
	if strings.Contains(u.EscapedPath(), "/./") || strings.Contains(u.EscapedPath(), "/../") ||
		strings.HasSuffix(u.EscapedPath(), "/.") || strings.HasSuffix(u.EscapedPath(), "/..") {
		return nil, ErrCIMDIneligible
	}
	return u, nil
}

func normalizeCIMDMetadata(req *oauthex.ClientRegistrationMetadata) {
	if len(req.GrantTypes) == 0 {
		req.GrantTypes = []string{oauthGrantAuthorizationCode}
	}
	if len(req.ResponseTypes) == 0 {
		req.ResponseTypes = []string{oauthCode}
	}
	if req.TokenEndpointAuthMethod == "" {
		req.TokenEndpointAuthMethod = tokenEndpointAuthMethodNone
	}
	req.Scope = normalizeScope(req.Scope, defaultRegisteredClientScope)
}

func validateCIMDDocument(clientID string, doc *cimdDocument) error {
	if doc.ClientID != clientID {
		return fmt.Errorf("%w: client_id must exactly match requested client_id", ErrCIMDInvalidMetadata)
	}
	if doc.ClientName == "" || len(doc.ClientName) > maxClientNameLen {
		return fmt.Errorf("%w: client_name is required", ErrCIMDInvalidMetadata)
	}
	if len(doc.Scope) > maxClientScopeLen {
		return fmt.Errorf("%w: scope must be at most %d bytes", ErrCIMDInvalidMetadata, maxClientScopeLen)
	}
	if len(doc.RedirectURIs) == 0 {
		return fmt.Errorf("%w: redirect_uris is required", ErrCIMDInvalidMetadata)
	}
	if err := validateRedirectURIs(doc.RedirectURIs); err != nil {
		return fmt.Errorf("%w: %v", ErrCIMDInvalidMetadata, err)
	}
	if doc.TokenEndpointAuthMethod != tokenEndpointAuthMethodNone {
		return fmt.Errorf("%w: only token_endpoint_auth_method=none is supported", ErrCIMDInvalidMetadata)
	}
	if doc.JWKS != "" || doc.JWKSURI != "" || doc.SoftwareStatement != "" {
		return fmt.Errorf("%w: key-based or signed client metadata is not supported", ErrCIMDInvalidMetadata)
	}
	if !slices.Contains(doc.GrantTypes, oauthGrantAuthorizationCode) {
		return fmt.Errorf("%w: authorization_code grant_type is required", ErrCIMDInvalidMetadata)
	}
	for _, grantType := range doc.GrantTypes {
		if grantType != oauthGrantAuthorizationCode && grantType != oauthGrantRefreshToken {
			return fmt.Errorf("%w: unsupported grant_type %q", ErrCIMDInvalidMetadata, grantType)
		}
	}
	for _, responseType := range doc.ResponseTypes {
		if responseType != oauthCode {
			return fmt.Errorf("%w: unsupported response_type %q", ErrCIMDInvalidMetadata, responseType)
		}
	}
	if !slices.Contains(doc.ResponseTypes, oauthCode) {
		return fmt.Errorf("%w: response_type code is required", ErrCIMDInvalidMetadata)
	}
	if err := validateSupportedScope(doc.Scope); err != nil {
		return fmt.Errorf("%w: %v", ErrCIMDInvalidMetadata, err)
	}
	return nil
}

func newCIMDHTTPClient(lookupIP func(ctx context.Context, network, host string) ([]net.IP, error)) *http.Client {
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := resolvePublicIPs(ctx, lookupIP, host)
			if err != nil {
				return nil, err
			}
			var dialer net.Dialer
			var lastErr error
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("no validated address available")
			}
			return nil, lastErr
		},
	}

	return &http.Client{
		Timeout:   defaultCIMDTimeoutSec * time.Second,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func resolvePublicIPs(ctx context.Context, lookupIP func(ctx context.Context, network, host string) ([]net.IP, error), host string) ([]net.IP, error) {
	ips, err := lookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("lookup client_id host: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("lookup client_id host: no addresses")
	}
	publicIPs := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if !isPublicRoutableIP(ip) {
			return nil, ErrCIMDIneligible
		}
		publicIPs = append(publicIPs, ip)
	}
	return publicIPs, nil
}

func isPublicRoutableIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	if addr.Is4In6() {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range deniedIPPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func isJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == contentTypeApplicationJSON || strings.HasSuffix(mediaType, "+json")
}

func mustParsePrefixes(raw ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			panic(err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}
