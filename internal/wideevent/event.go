package wideevent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/wolfeidau/starlogz/internal/clientclass"
	"github.com/wolfeidau/starlogz/internal/ctxlog"
	"go.opentelemetry.io/otel/trace"
)

const (
	SchemaVersion  = 1
	publishTimeout = 400 * time.Millisecond
)

type Name string

const (
	OAuthAuthorizationCompleted      Name = "oauth.authorization.completed"
	OAuthClientRegistrationCompleted Name = "oauth.client_registration.completed"
	OAuthGitHubCallbackCompleted     Name = "oauth.github_callback.completed"
	OAuthTokenExchangeCompleted      Name = "oauth.token_exchange.completed" //nolint:gosec // Event name, not a credential.
	OAuthRefreshCompleted            Name = "oauth.refresh.completed"
	UILoginCompleted                 Name = "ui.login.completed"
	UISessionCreated                 Name = "ui.session.created"
	UISessionRevoked                 Name = "ui.session.revoked"
	MCPClientInitialized             Name = "mcp.client_initialized"
	MCPToolCallCompleted             Name = "mcp.tool_call.completed"
)

const (
	AttributeTool                     = "tool"
	AttributeResultCountBucket        = "result_count_bucket"
	AttributeClientProduct            = "client_product"
	AttributeClientProductMajor       = "client_product_major"
	AttributeClientIdentitySource     = "client_identity_source"
	AttributeClientIdentityConfidence = "client_identity_confidence"
	ToolWhoami                        = "whoami"
	ToolProjectEnsure                 = "project_ensure"
	ToolProjectList                   = "project_list"
	ToolInsightWrite                  = "insight_write"
	ToolInsightGet                    = "insight_get"
	ToolInsightSearch                 = "insight_search"
	ToolInsightList                   = "insight_list"
	ToolInsightUpdate                 = "insight_update"
	ToolInsightDelete                 = "insight_delete"
	ToolInsightListTags               = "insight_list_tags"
)

const (
	ResultCountZero       = "0"
	ResultCountOneToTen   = "1-10"
	ResultCountElevenTo50 = "11-50"
	ResultCount51To100    = "51-100"
	ResultCount101To200   = "101-200"
)

type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

const (
	ReasonCompleted        = "completed"
	ReasonInvalidRequest   = "invalid_request"
	ReasonUnauthorized     = "unauthorized"
	ReasonNotFound         = "not_found"
	ReasonMethodNotAllowed = "method_not_allowed"
	ReasonThrottled        = "throttled"
	ReasonUpstreamError    = "upstream_error"
	ReasonServerError      = "server_error"
	ReasonFailed           = "failed"
)

type Event struct {
	SchemaVersion  int               `json:"schema_version"`
	EventID        string            `json:"event_id"`
	EventName      Name              `json:"event_name"`
	OccurredAt     string            `json:"occurred_at"`
	Environment    string            `json:"environment"`
	ServiceVersion string            `json:"service_version"`
	RequestID      string            `json:"request_id,omitempty"`
	TraceID        string            `json:"trace_id,omitempty"`
	Outcome        Outcome           `json:"outcome"`
	Reason         string            `json:"reason"`
	DurationMS     int64             `json:"duration_ms"`
	Attributes     map[string]string `json:"attributes,omitempty"`
}

type EventPublisher interface {
	Publish(context.Context, Event) error
}

type NoopPublisher struct{}

func (NoopPublisher) Publish(context.Context, Event) error { return nil }

type Emitter struct {
	publisher      EventPublisher
	environment    string
	serviceVersion string
	logger         *slog.Logger
}

func NewEmitter(publisher EventPublisher, environment, serviceVersion string, logger *slog.Logger) (*Emitter, error) {
	if publisher == nil {
		publisher = NoopPublisher{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	emitter := &Emitter{
		publisher: publisher, environment: environment, serviceVersion: serviceVersion,
		logger: logger.With(slog.String("component", "wideevent")),
	}
	probe := Event{Environment: environment, ServiceVersion: serviceVersion}
	if err := validateDeploymentFields(probe); err != nil {
		return nil, err
	}
	return emitter, nil
}

func NewNoopEmitter() *Emitter {
	emitter, err := NewEmitter(NoopPublisher{}, "local", "devel", nil)
	if err != nil {
		panic(err)
	}
	return emitter
}

func (e *Emitter) Completion(ctx context.Context, name Name, outcome Outcome, reason string, started time.Time, attributes map[string]string) {
	event := Event{
		SchemaVersion:  SchemaVersion,
		EventID:        uuid.New().String(),
		EventName:      name,
		OccurredAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Environment:    e.environment,
		ServiceVersion: e.serviceVersion,
		RequestID:      ctxlog.RequestIDFrom(ctx),
		Outcome:        outcome,
		Reason:         reason,
		DurationMS:     max(time.Since(started).Milliseconds(), 0),
		Attributes:     attributes,
	}
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		event.TraceID = spanContext.TraceID().String()
	}
	if err := event.Validate(); err != nil {
		e.logger.WarnContext(ctx, "wide event rejected", slog.String("event_name", string(name)), slog.Any("error", err))
		return
	}

	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := e.publisher.Publish(publishCtx, event); err != nil {
		e.logger.WarnContext(ctx, "wide event publish failed", slog.String("event_name", string(name)), slog.Any("error", err))
	}
}

func (e *Emitter) HTTPHandler(name Name, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		collector := newAttributeCollector(name)
		ctx := context.WithValue(r.Context(), attributeCollectorKey{}, collector)
		r = r.WithContext(ctx)
		started := time.Now()
		rw := &statusWriter{ResponseWriter: w}
		defer func(ctx context.Context) {
			if recovered := recover(); recovered != nil {
				e.Completion(ctx, name, OutcomeFailure, ReasonServerError, started, collector.attributes)
				panic(recovered)
			}
			outcome, reason := classifyStatus(rw.statusCode())
			e.Completion(ctx, name, outcome, reason, started, collector.attributes)
		}(ctx)
		next.ServeHTTP(rw, r)
	})
}

type attributeCollectorKey struct{}

type attributeCollector struct {
	attributes map[string]string
}

func newAttributeCollector(name Name) *attributeCollector {
	collector := &attributeCollector{}
	if eventRequiresIdentity(name) {
		collector.attributes = ClientIdentityAttributes(clientclass.Unknown())
	}
	return collector
}

func SetClientIdentity(ctx context.Context, identity clientclass.Classification) {
	collector, _ := ctx.Value(attributeCollectorKey{}).(*attributeCollector)
	if collector == nil {
		return
	}
	collector.attributes = ClientIdentityAttributes(identity)
}

func ClientIdentityAttributes(identity clientclass.Classification) map[string]string {
	identity = clientclass.Normalize(identity)
	attributes := map[string]string{
		AttributeClientProduct:            identity.Product,
		AttributeClientIdentitySource:     identity.Source,
		AttributeClientIdentityConfidence: identity.Confidence,
	}
	if identity.HasMajor {
		attributes[AttributeClientProductMajor] = strconv.Itoa(identity.Major)
	}
	return attributes
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func classifyStatus(status int) (Outcome, string) {
	if status < http.StatusBadRequest {
		return OutcomeSuccess, ReasonCompleted
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return OutcomeFailure, ReasonUnauthorized
	case http.StatusNotFound:
		return OutcomeFailure, ReasonNotFound
	case http.StatusMethodNotAllowed:
		return OutcomeFailure, ReasonMethodNotAllowed
	case http.StatusTooManyRequests:
		return OutcomeFailure, ReasonThrottled
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return OutcomeFailure, ReasonUpstreamError
	default:
		if status >= http.StatusInternalServerError {
			return OutcomeFailure, ReasonServerError
		}
		return OutcomeFailure, ReasonInvalidRequest
	}
}

var (
	deploymentValuePattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
	allowedNames           = map[Name]struct{}{
		OAuthAuthorizationCompleted: {}, OAuthClientRegistrationCompleted: {},
		OAuthGitHubCallbackCompleted: {},
		OAuthTokenExchangeCompleted:  {}, OAuthRefreshCompleted: {},
		UILoginCompleted: {}, UISessionCreated: {}, UISessionRevoked: {},
		MCPClientInitialized: {}, MCPToolCallCompleted: {},
	}
	allowedReasons = map[string]struct{}{
		ReasonCompleted: {}, ReasonInvalidRequest: {}, ReasonUnauthorized: {},
		ReasonNotFound: {}, ReasonMethodNotAllowed: {}, ReasonThrottled: {},
		ReasonUpstreamError: {}, ReasonServerError: {}, ReasonFailed: {},
	}
	allowedTools = map[string]struct{}{
		ToolWhoami: {}, ToolProjectEnsure: {}, ToolProjectList: {},
		ToolInsightWrite: {}, ToolInsightGet: {}, ToolInsightSearch: {}, ToolInsightList: {},
		ToolInsightUpdate: {}, ToolInsightDelete: {}, ToolInsightListTags: {},
	}
	allowedResultCountBuckets = map[string]struct{}{
		ResultCountZero: {}, ResultCountOneToTen: {}, ResultCountElevenTo50: {},
		ResultCount51To100: {}, ResultCount101To200: {},
	}
)

func (e Event) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %d", SchemaVersion)
	}
	if _, err := uuid.Parse(e.EventID); err != nil {
		return fmt.Errorf("event_id must be a UUID: %w", err)
	}
	if _, ok := allowedNames[e.EventName]; !ok {
		return fmt.Errorf("unsupported event_name %q", e.EventName)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err != nil {
		return fmt.Errorf("occurred_at must be RFC3339: %w", err)
	}
	if err := validateDeploymentFields(e); err != nil {
		return err
	}
	if e.RequestID != "" {
		if _, err := uuid.Parse(e.RequestID); err != nil {
			return fmt.Errorf("request_id must be a UUID: %w", err)
		}
	}
	if e.TraceID != "" {
		if _, err := trace.TraceIDFromHex(e.TraceID); err != nil {
			return fmt.Errorf("trace_id must be valid: %w", err)
		}
	}
	if e.Outcome != OutcomeSuccess && e.Outcome != OutcomeFailure {
		return fmt.Errorf("unsupported outcome %q", e.Outcome)
	}
	if _, ok := allowedReasons[e.Reason]; !ok {
		return fmt.Errorf("unsupported reason %q", e.Reason)
	}
	if e.Outcome == OutcomeSuccess && e.Reason != ReasonCompleted {
		return fmt.Errorf("successful events must use reason %q", ReasonCompleted)
	}
	if e.Outcome == OutcomeFailure && e.Reason == ReasonCompleted {
		return fmt.Errorf("failed events must not use reason %q", ReasonCompleted)
	}
	if e.DurationMS < 0 {
		return fmt.Errorf("duration_ms must not be negative")
	}
	return validateAttributes(e.EventName, e.Outcome, e.Attributes)
}

func validateDeploymentFields(e Event) error {
	if !deploymentValuePattern.MatchString(e.Environment) {
		return fmt.Errorf("environment must be a bounded deployment identifier")
	}
	if !deploymentValuePattern.MatchString(e.ServiceVersion) {
		return fmt.Errorf("service_version must be a bounded deployment identifier")
	}
	return nil
}

func validateAttributes(name Name, outcome Outcome, attributes map[string]string) error {
	if eventRequiresIdentity(name) {
		if err := validateIdentityAttributes(attributes); err != nil {
			return err
		}
	}
	switch name {
	case MCPToolCallCompleted:
		return validateToolAttributes(outcome, attributes)
	case MCPClientInitialized, OAuthClientRegistrationCompleted, OAuthAuthorizationCompleted,
		OAuthGitHubCallbackCompleted, OAuthTokenExchangeCompleted, OAuthRefreshCompleted:
		for key := range attributes {
			if !identityAttribute(key) {
				return fmt.Errorf("unsupported attribute %q", key)
			}
		}
		return nil
	default:
		if len(attributes) != 0 {
			return fmt.Errorf("attributes are not allowed for %q", name)
		}
		return nil
	}
}

func validateToolAttributes(outcome Outcome, attributes map[string]string) error {
	tool, ok := attributes[AttributeTool]
	if !ok {
		return fmt.Errorf("tool attribute is required")
	}
	if _, ok := allowedTools[tool]; !ok {
		return fmt.Errorf("unsupported tool attribute %q", tool)
	}
	for key := range attributes {
		if key != AttributeTool && key != AttributeResultCountBucket && !identityAttribute(key) {
			return fmt.Errorf("unsupported attribute %q", key)
		}
	}
	bucket, hasBucket := attributes[AttributeResultCountBucket]
	countedTool := tool == ToolInsightSearch || tool == ToolInsightList
	if outcome == OutcomeSuccess && countedTool && !hasBucket {
		return fmt.Errorf("result_count_bucket is required for successful %q events", tool)
	}
	if hasBucket && (outcome != OutcomeSuccess || !countedTool) {
		return fmt.Errorf("result_count_bucket is not allowed for this event")
	}
	if hasBucket {
		if _, ok := allowedResultCountBuckets[bucket]; !ok {
			return fmt.Errorf("unsupported result_count_bucket %q", bucket)
		}
	}
	return nil
}

func validateIdentityAttributes(attributes map[string]string) error {
	product, hasProduct := attributes[AttributeClientProduct]
	source, hasSource := attributes[AttributeClientIdentitySource]
	confidence, hasConfidence := attributes[AttributeClientIdentityConfidence]
	if !hasProduct || !hasSource || !hasConfidence {
		return fmt.Errorf("client identity attributes are required")
	}
	identity := clientclass.Classification{
		Product: product, Source: source, Confidence: confidence,
	}
	if value, ok := attributes[AttributeClientProductMajor]; ok {
		major, err := strconv.Atoi(value)
		if err != nil || strconv.Itoa(major) != value {
			return fmt.Errorf("client_product_major must be a canonical integer")
		}
		identity.Major = major
		identity.HasMajor = true
	}
	if !clientclass.Valid(identity) {
		return fmt.Errorf("unsupported client identity attributes")
	}
	return nil
}

func eventRequiresIdentity(name Name) bool {
	switch name {
	case MCPClientInitialized, MCPToolCallCompleted, OAuthClientRegistrationCompleted,
		OAuthAuthorizationCompleted, OAuthGitHubCallbackCompleted,
		OAuthTokenExchangeCompleted, OAuthRefreshCompleted:
		return true
	default:
		return false
	}
}

func identityAttribute(key string) bool {
	switch key {
	case AttributeClientProduct, AttributeClientProductMajor,
		AttributeClientIdentitySource, AttributeClientIdentityConfidence:
		return true
	default:
		return false
	}
}
