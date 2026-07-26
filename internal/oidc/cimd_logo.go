package oidc

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
)

const (
	maxCIMDLogoURILen       = 2048
	maxCIMDLogoResponseSize = 256 << 10
	maxCIMDLogoOutputSize   = 96 << 10
	maxCIMDLogoDimension    = 1024
	maxCIMDLogoPixels       = 1_000_000
	maxCIMDLogoDisplaySize  = 128
	cimdLogoTimeout         = 2 * time.Second
	cimdLogoMediaTypePNG    = "image/png"
	cimdLogoMediaTypeJPEG   = "image/jpeg"
)

const (
	cimdLogoOmittedInvalidURL  = "invalid_url"
	cimdLogoOmittedNetwork     = "network"
	cimdLogoOmittedStatus      = "status"
	cimdLogoOmittedContentType = "content_type"
	cimdLogoOmittedSize        = "size"
	cimdLogoOmittedDimensions  = "dimensions"
	cimdLogoOmittedDecode      = "decode"
)

func (r *httpClientIDMetadataResolver) resolveLogo(ctx context.Context, rawURL string) ([]byte, string) {
	if rawURL == "" {
		return nil, ""
	}
	logoURL, err := parseEligibleCIMDLogoURL(rawURL)
	if err != nil {
		return nil, cimdLogoOmittedInvalidURL
	}
	logoCtx, cancel := context.WithTimeout(ctx, cimdLogoTimeout)
	defer cancel()
	if _, err := resolvePublicIPs(logoCtx, r.lookupIP, logoURL.Hostname()); err != nil {
		return nil, cimdLogoOmittedNetwork
	}

	req, err := http.NewRequestWithContext(logoCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, cimdLogoOmittedInvalidURL
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, cimdLogoOmittedNetwork
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, cimdLogoOmittedStatus
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mediaType != cimdLogoMediaTypePNG && mediaType != cimdLogoMediaTypeJPEG) {
		return nil, cimdLogoOmittedContentType
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCIMDLogoResponseSize+1))
	if err != nil {
		return nil, cimdLogoOmittedNetwork
	}
	if len(body) > maxCIMDLogoResponseSize {
		return nil, cimdLogoOmittedSize
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, cimdLogoOmittedDecode
	}
	if (mediaType == cimdLogoMediaTypePNG && format != "png") || (mediaType == cimdLogoMediaTypeJPEG && format != "jpeg") {
		return nil, cimdLogoOmittedContentType
	}
	if config.Width <= 0 || config.Height <= 0 ||
		config.Width > maxCIMDLogoDimension || config.Height > maxCIMDLogoDimension ||
		config.Width*config.Height > maxCIMDLogoPixels {
		return nil, cimdLogoOmittedDimensions
	}

	src, format, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, cimdLogoOmittedDecode
	}
	if (mediaType == cimdLogoMediaTypePNG && format != "png") || (mediaType == cimdLogoMediaTypeJPEG && format != "jpeg") {
		return nil, cimdLogoOmittedContentType
	}
	dstWidth, dstHeight := scaledLogoDimensions(config.Width, config.Height)
	dst := image.NewNRGBA(image.Rect(0, 0, dstWidth, dstHeight))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)

	var canonical bytes.Buffer
	if err := png.Encode(&canonical, dst); err != nil {
		return nil, cimdLogoOmittedDecode
	}
	if canonical.Len() > maxCIMDLogoOutputSize {
		return nil, cimdLogoOmittedSize
	}
	return canonical.Bytes(), ""
}

func parseEligibleCIMDLogoURL(rawURL string) (*url.URL, error) {
	if rawURL == "" || len(rawURL) > maxCIMDLogoURILen {
		return nil, ErrCIMDIneligible
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, ErrCIMDIneligible
	}
	if !u.IsAbs() || u.Scheme != redirectSchemeHTTPS || u.Hostname() == "" || u.User != nil {
		return nil, ErrCIMDIneligible
	}
	if u.Fragment != "" || u.Port() != "" || net.ParseIP(u.Hostname()) != nil {
		return nil, ErrCIMDIneligible
	}
	if u.EscapedPath() == "" || !strings.HasPrefix(u.EscapedPath(), "/") {
		return nil, ErrCIMDIneligible
	}
	return u, nil
}

func scaledLogoDimensions(width, height int) (int, int) {
	if width <= maxCIMDLogoDisplaySize && height <= maxCIMDLogoDisplaySize {
		return width, height
	}
	if width >= height {
		return maxCIMDLogoDisplaySize, max(1, height*maxCIMDLogoDisplaySize/width)
	}
	return max(1, width*maxCIMDLogoDisplaySize/height), maxCIMDLogoDisplaySize
}
