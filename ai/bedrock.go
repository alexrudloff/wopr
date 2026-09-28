package ai

// Amazon Bedrock provider. Uses the AWS SDK Go v2 bedrockruntime client and its ConverseStream
// API. Auth resolves through the default AWS credentials chain (env
// vars, shared config, IMDS, SSO, etc.) with an optional bearer-token
// fast path via AWS_BEARER_TOKEN_BEDROCK.
//
// Supported:
//   - ConverseStream end-to-end: text, tool calls, thinking,
//     metadata/usage, stop reason mapping.
//   - Message conversion (user text/image, assistant text/toolCall/thinking, toolResult
//     coalescing into a single user message).
//   - Tool configuration with toolSpec + JSON input schema.
//   - System prompt with optional CachePoint for prompt-cache-aware
//     Claude models (gated by WOPR_CACHE_RETENTION).
//   - Adaptive vs enabled thinking via additionalModelRequestFields
//     for Claude models (buildBedrockAdditionalFields).
//   - Bedrock error name → human-readable prefix mapping for retry
//     classifiers downstream (BEDROCK_ERROR_PREFIXES).
//   - Region resolution: explicit > AWS_REGION > AWS_DEFAULT_REGION >
//     endpoint hostname > us-east-1 when no profile is set.
//
// Known deferred bits (clearly bounded, do not affect the common path):
//   - HTTP/HTTPS proxy installation (HTTP_PROXY/HTTPS_PROXY): the SDK
//     honours its own proxy env vars at the transport layer; there is no
//     custom proxy agent. Revisit if a user reports a proxy-only environment.
//   - AWS_BEDROCK_FORCE_HTTP1: the SDK uses HTTP/2 by default. Add when we have a custom
//     endpoint that requires HTTP/1.1.
//   - requestMetadata cost-allocation tags: accepted in StreamOptions
//     but not yet plumbed into the ConverseStream input.
//   - GovCloud thinking-display filtering: not gated.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	btypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	smithybearer "github.com/aws/smithy-go/auth/bearer"
	smithymiddleware "github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// BedrockProvider implements the Provider interface backed by AWS
// Bedrock's ConverseStream API.
type BedrockProvider struct {
	model     string
	modelName string
	baseURL   string
}

// NewBedrockProvider constructs a Bedrock provider for the given model id,
// forwarded verbatim to ConverseStream (a base model id or an
// inference-profile ARN). modelName is optional display metadata, used only
// for capability detection on application inference profiles whose ARN does
// not reveal the underlying Claude model family.
func NewBedrockProvider(model, modelName, baseURL string) *BedrockProvider {
	return &BedrockProvider{model: model, modelName: modelName, baseURL: baseURL}
}

// ID returns the provider identifier.
func (p *BedrockProvider) ID() string { return "amazon-bedrock" }

// Close releases any persistent resources held by the provider. The
// bedrockruntime client maintains no goroutines or sockets that
// outlive a call, so this is a no-op.
func (p *BedrockProvider) Close() error { return nil }

// Stream runs ConverseStream and translates its event stream into the
// wopr AssistantMessageEvent channel.
func (p *BedrockProvider) Stream(ctx context.Context, transcript TranscriptContext, opts StreamOptions) (*AssistantMessageEventStream, error) {
	if err := validateProviderRequest(ctx, transcript); err != nil {
		return nil, fmt.Errorf("amazon-bedrock: invalid transcript: %w", err)
	}
	cfg, err := loadBedrockConfig(ctx, p.baseURL, p.model, opts.Env)
	if err != nil {
		return nil, fmt.Errorf("amazon-bedrock: %w", err)
	}
	client := bedrockruntime.NewFromConfig(cfg)

	modelMeta := &Model{ID: p.model, DisplayName: p.modelName}
	if generated, ok := LookupModel(p.ID() + "/" + p.model); ok {
		modelMeta = generated.ToModel()
	}
	if modelMeta.DisplayName == "" {
		modelMeta.DisplayName = p.modelName
	}
	supportsMidConversation := modelMeta.ProviderMeta.Compat != nil &&
		modelMeta.ProviderMeta.Compat.SupportsMidConvoSystemMessages != nil &&
		*modelMeta.ProviderMeta.Compat.SupportsMidConvoSystemMessages
	resolved := ResolveTranscript(transcript, supportsMidConversation)
	messages := resolved.Messages()
	cacheRetention := resolveBedrockCacheRetentionWithEnv(opts.Env)
	system := buildBedrockSystemPrompt(p.model, p.modelName, GetCurrentSystemPrompt(messages[:min(1, len(messages))]), cacheRetention)
	conversation := WithoutInitialSystemMessage(messages)
	convertedMessages, err := convertBedrockMessages(conversation, p.model, p.modelName, cacheRetention)
	if err != nil {
		return nil, fmt.Errorf("amazon-bedrock: convert messages: %w", err)
	}
	supportsStrictTools := modelMeta.ProviderMeta.Compat != nil && modelMeta.ProviderMeta.Compat.SupportsStrictMode != nil && *modelMeta.ProviderMeta.Compat.SupportsStrictMode
	tools, err := convertBedrockTools(GetCurrentTools(messages), supportsStrictTools)
	if err != nil {
		return nil, fmt.Errorf("amazon-bedrock: convert tools: %w", err)
	}

	inf := &btypes.InferenceConfiguration{}
	if opts.MaxTokens > 0 {
		mt := int32(opts.MaxTokens)
		inf.MaxTokens = &mt
	}
	if opts.TemperatureSet || opts.Temperature != 0 {
		t := float32(opts.Temperature)
		inf.Temperature = &t
	}

	input := &bedrockruntime.ConverseStreamInput{
		ModelId:         aws.String(p.model),
		Messages:        convertedMessages,
		System:          system,
		InferenceConfig: inf,
		ToolConfig:      tools,
	}
	if extra := buildBedrockAdditionalFields(modelMeta, p.modelName, opts); extra != nil {
		input.AdditionalModelRequestFields = bdoc.NewLazyDocument(extra)
	}
	if opts.OnPayload != nil {
		next, err := opts.OnPayload(input, modelMeta)
		if err != nil {
			return nil, fmt.Errorf("amazon-bedrock: onPayload: %w", err)
		}
		switch v := next.(type) {
		case nil:
		case *bedrockruntime.ConverseStreamInput:
			input = v
		case bedrockruntime.ConverseStreamInput:
			input = &v
		default:
			return nil, fmt.Errorf("amazon-bedrock: onPayload returned %T, want *bedrockruntime.ConverseStreamInput", next)
		}
	}

	resp, err := client.ConverseStream(ctx, input, func(options *bedrockruntime.Options) {
		if len(opts.Headers) > 0 {
			options.APIOptions = append(options.APIOptions, withBedrockHeaders(opts.Headers))
		}
	})
	if err != nil {
		err = mapBedrockTransportError(ctx, err, false)
		return nil, fmt.Errorf("amazon-bedrock: %s", formatBedrockError(err))
	}

	builder := newAssistantStreamBuilder(ctx, APIBedrockConverseStream, p.ID(), p.model, opts.ModelCost)
	go p.parseBedrockStream(ctx, resp, builder)
	return builder.stream, nil
}

func maxTokensPtr(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

// ─── Region / endpoint / auth resolution ─────────────────────────────────

// loadBedrockConfig builds an aws.Config honouring the same env vars
// and precedence rules: option region > AWS_REGION >
// AWS_DEFAULT_REGION > region parsed from base URL > us-east-1 (when
// no AWS_PROFILE is set). Honours AWS_BEARER_TOKEN_BEDROCK and
// AWS_BEDROCK_SKIP_AUTH.
func loadBedrockConfig(ctx context.Context, baseURL, modelID string, env ProviderEnv) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{}

	region := resolveBedrockRegionWithEnv(baseURL, modelID, env)
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if trimmed := strings.TrimSpace(baseURL); trimmed != "" {
		opts = append(opts, awsconfig.WithBaseEndpoint(strings.TrimRight(trimmed, "/")))
	}

	// Test-only auth bypass.
	if getProviderEnvValue("AWS_BEDROCK_SKIP_AUTH", env) == "1" {
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			"dummy-access-key", "dummy-secret-key", "",
		)))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS config: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	// Bearer-token auth alternative. The SDK signs every request with
	// SigV4 by default; setting the bearer token provider lets the SDK
	// emit `Authorization: Bearer …` instead.
	if tok := getProviderEnvValue("AWS_BEARER_TOKEN_BEDROCK", env); tok != "" && getProviderEnvValue("AWS_BEDROCK_SKIP_AUTH", env) != "1" {
		cfg.BearerAuthTokenProvider = staticBearerTokenProvider(tok)
	}
	return cfg, nil
}

// bedrockARNRegionRE extracts the region embedded in an inference-profile ARN
// (e.g. arn:aws:bedrock:us-west-2:...).
var bedrockARNRegionRE = regexp.MustCompile(`^arn:aws(?:-[a-z0-9-]+)?:bedrock:([a-z0-9-]+):`)

func resolveBedrockRegionWithEnv(baseURL, modelID string, env ProviderEnv) string {
	// An inference-profile ARN's embedded region wins over AWS_REGION and the
	// rest of the chain (avoids conflicts with AWS_REGION set for other services).
	if m := bedrockARNRegionRE.FindStringSubmatch(modelID); m != nil {
		return m[1]
	}
	if r := getProviderEnvValue("AWS_REGION", env); r != "" {
		return r
	}
	if r := getProviderEnvValue("AWS_DEFAULT_REGION", env); r != "" {
		return r
	}
	if r := bedrockEndpointRegion(baseURL); r != "" {
		return r
	}
	if getProviderEnvValue("AWS_PROFILE", env) != "" {
		// Let the SDK resolve region from the profile's config.
		return ""
	}
	return "us-east-1"
}

var bedrockEndpointHostRE = regexp.MustCompile(`^bedrock-runtime(-fips)?\.([a-z0-9-]+)\.amazonaws\.com(\.cn)?$`)

func bedrockEndpointRegion(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	m := bedrockEndpointHostRE.FindStringSubmatch(strings.ToLower(u.Hostname()))
	if len(m) < 3 {
		return ""
	}
	return m[2]
}

// staticBearerTokenProvider implements aws.BearerTokenProvider with a
// fixed value.
type staticBearerTokenProvider string

func (s staticBearerTokenProvider) RetrieveBearerToken(_ context.Context) (smithybearer.Token, error) {
	return smithybearer.Token{Value: string(s)}, nil
}

type bedrockHeadersMiddleware struct {
	headers ProviderHeaders
}

func (middleware bedrockHeadersMiddleware) ID() string { return "WoprProviderHeaders" }

func isReservedBedrockHeader(name string) bool {
	name = strings.ToLower(name)
	return name == "authorization" || name == "host" || strings.HasPrefix(name, "x-amz-")
}

func (middleware bedrockHeadersMiddleware) HandleBuild(ctx context.Context, input smithymiddleware.BuildInput, next smithymiddleware.BuildHandler) (smithymiddleware.BuildOutput, smithymiddleware.Metadata, error) {
	request, ok := input.Request.(*smithyhttp.Request)
	if !ok {
		return smithymiddleware.BuildOutput{}, smithymiddleware.Metadata{}, fmt.Errorf("bedrock provider headers: unexpected request type %T", input.Request)
	}
	for name, value := range middleware.headers {
		if isReservedBedrockHeader(name) {
			continue
		}
		if value == nil {
			request.Header.Del(name)
			continue
		}
		request.Header.Set(name, *value)
	}
	return next.HandleBuild(ctx, input)
}

func withBedrockHeaders(headers ProviderHeaders) func(*smithymiddleware.Stack) error {
	return func(stack *smithymiddleware.Stack) error {
		return stack.Build.Add(bedrockHeadersMiddleware{headers: headers}, smithymiddleware.After)
	}
}

// ─── Cache retention ─────────────────────────────────────────────────────

type bedrockCacheRetention string

const (
	bedrockCacheNone  bedrockCacheRetention = "none"
	bedrockCacheShort bedrockCacheRetention = "short"
	bedrockCacheLong  bedrockCacheRetention = "long"
)

// bedrockEmptyPlaceholder replaces user/tool-result content emptied by
// blank-text or surrogate filtering. Bedrock rejects empty content blocks,
// so the placeholder preserves the turn (and keeps a tool_use paired with
// its tool_result).
const bedrockEmptyPlaceholder = "<empty>"

func resolveBedrockCacheRetentionWithEnv(env ProviderEnv) bedrockCacheRetention {
	switch getProviderEnvValue("WOPR_CACHE_RETENTION", env) {
	case "long":
		return bedrockCacheLong
	case "none":
		return bedrockCacheNone
	default:
		return bedrockCacheShort
	}
}

func bedrockModelMatchCandidates(modelID, modelName string) []string {
	values := []string{modelID}
	if strings.TrimSpace(modelName) != "" {
		values = append(values, modelName)
	}
	out := make([]string, 0, len(values)*2)
	for _, value := range values {
		lower := strings.ToLower(strings.TrimSpace(value))
		if lower == "" {
			continue
		}
		out = append(out, lower)
		normalized := strings.NewReplacer(" ", "-", "_", "-", ".", "-", ":", "-").Replace(lower)
		if normalized != lower {
			out = append(out, normalized)
		}
	}
	return out
}

func isAnthropicClaudeBedrockModel(modelID, modelName string) bool {
	for _, candidate := range bedrockModelMatchCandidates(modelID, modelName) {
		if strings.Contains(candidate, "anthropic.claude") || strings.Contains(candidate, "anthropic/claude") || strings.Contains(candidate, "claude") {
			return true
		}
	}
	return false
}

func supportsBedrockPromptCachingWithName(modelID, modelName string) bool {
	candidates := bedrockModelMatchCandidates(modelID, modelName)
	hasClaudeRef := false
	for _, candidate := range candidates {
		if strings.Contains(candidate, "claude") {
			hasClaudeRef = true
			break
		}
	}
	if !hasClaudeRef {
		// Allow forcing for application inference profiles whose ARNs
		// don't reveal the model name.
		return os.Getenv("AWS_BEDROCK_FORCE_CACHE") == "1"
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "fable-5") || strings.Contains(candidate, "sonnet-5") {
			return true
		}
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "-4-") || strings.Contains(candidate, "claude-3-7-sonnet") || strings.Contains(candidate, "claude-3-5-haiku") {
			return true
		}
	}
	return false
}

func supportsBedrockThinkingSignatureWithName(modelID, modelName string) bool {
	return isAnthropicClaudeBedrockModel(modelID, modelName)
}

func supportsBedrockAdaptiveThinkingWithName(modelID, modelName string) bool {
	for _, candidate := range bedrockModelMatchCandidates(modelID, modelName) {
		if strings.Contains(candidate, "opus-4-6") || strings.Contains(candidate, "opus-4-7") || strings.Contains(candidate, "opus-4-8") || strings.Contains(candidate, "sonnet-4-6") || strings.Contains(candidate, "sonnet-5") || strings.Contains(candidate, "fable-5") {
			return true
		}
	}
	return false
}

// ─── Message conversion ──────────────────────────────────────────────────

func buildBedrockSystemPrompt(modelID, modelName, systemPrompt string, cacheRetention bedrockCacheRetention) []btypes.SystemContentBlock {
	if systemPrompt == "" {
		return nil
	}
	blocks := []btypes.SystemContentBlock{
		&btypes.SystemContentBlockMemberText{Value: systemPrompt},
	}
	if cacheRetention != bedrockCacheNone && supportsBedrockPromptCachingWithName(modelID, modelName) {
		blocks = append(blocks, &btypes.SystemContentBlockMemberCachePoint{
			Value: btypes.CachePointBlock{
				Type: btypes.CachePointTypeDefault,
				Ttl:  bedrockCacheTTL(cacheRetention),
			},
		})
	}
	return blocks
}

func bedrockCacheTTL(retention bedrockCacheRetention) btypes.CacheTTL {
	if retention == bedrockCacheLong {
		return btypes.CacheTTLOneHour
	}
	return ""
}

// convertBedrockMessages converts the transcript, normalizing tool ids and
// coalescing consecutive tool results into the single user message that
// Bedrock requires.
func convertBedrockMessages(msgs []Message, modelID, modelName string, cacheRetention bedrockCacheRetention) ([]btypes.Message, error) {
	out := make([]btypes.Message, 0, len(msgs))

	for i := 0; i < len(msgs); i++ {
		switch message := msgs[i].(type) {
		case SystemMessage:
			text := RenderSystemMessageUpdate(message)
			if strings.TrimSpace(text) != "" {
				out = append(out, btypes.Message{Role: btypes.ConversationRoleUser, Content: []btypes.ContentBlock{
					&btypes.ContentBlockMemberText{Value: text},
				}})
			}
		case UserMessage:
			blocks, err := bedrockUserContent(message.Content)
			if err != nil {
				return nil, err
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, btypes.Message{Role: btypes.ConversationRoleUser, Content: blocks})

		case AssistantMessage:
			blocks, err := bedrockAssistantContent(message.Content, modelID, modelName)
			if err != nil {
				return nil, err
			}
			if len(blocks) == 0 {
				// Skip empty assistant messages.
				continue
			}
			out = append(out, btypes.Message{Role: btypes.ConversationRoleAssistant, Content: blocks})

		case ToolResultMessage:
			// Collect this tool-result + all immediately following
			// tool-result messages into a single user message.
			combined, consumed, err := bedrockToolResultRun(msgs, i)
			if err != nil {
				return nil, err
			}
			if len(combined) > 0 {
				out = append(out, btypes.Message{Role: btypes.ConversationRoleUser, Content: combined})
			}
			i += consumed - 1

		default:
			continue
		}
	}

	// Final cache point on the last user message.
	if cacheRetention != bedrockCacheNone && supportsBedrockPromptCachingWithName(modelID, modelName) && len(out) > 0 {
		last := &out[len(out)-1]
		if last.Role == btypes.ConversationRoleUser {
			last.Content = append(last.Content, &btypes.ContentBlockMemberCachePoint{
				Value: btypes.CachePointBlock{Type: btypes.CachePointTypeDefault, Ttl: bedrockCacheTTL(cacheRetention)},
			})
		}
	}
	return out, nil
}

func bedrockUserContent(content UserContent) ([]btypes.ContentBlock, error) {
	switch content := content.(type) {
	case UserText:
		text := string(content)
		if strings.TrimSpace(text) == "" {
			text = bedrockEmptyPlaceholder
		}
		return []btypes.ContentBlock{
			&btypes.ContentBlockMemberText{Value: text},
		}, nil
	case UserContentBlocks:
		out := make([]btypes.ContentBlock, 0, len(content))
		for _, block := range content {
			switch block := block.(type) {
			case TextContent:
				text := block.Text
				if strings.TrimSpace(text) == "" {
					continue
				}
				out = append(out, &btypes.ContentBlockMemberText{Value: text})
			case ImageContent:
				img, err := bedrockImageBlock(block.MimeType, block.Data)
				if err != nil {
					return nil, err
				}
				out = append(out, &btypes.ContentBlockMemberImage{Value: img})
			default:
				continue
			}
		}
		// Bedrock rejects empty content; replace a user message emptied by
		// filtering (blank/unknown blocks) with a placeholder rather than
		// dropping the turn.
		if len(out) == 0 {
			out = append(out, &btypes.ContentBlockMemberText{Value: bedrockEmptyPlaceholder})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported user content shape %T", content)
	}
}

func bedrockAssistantContent(blocks []AssistantContentBlock, modelID, modelName string) ([]btypes.ContentBlock, error) {
	out := make([]btypes.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		switch block := block.(type) {
		case TextContent:
			if strings.TrimSpace(block.Text) == "" {
				continue
			}
			out = append(out, &btypes.ContentBlockMemberText{Value: block.Text})
		case ToolCall:
			arguments := block.Arguments
			if arguments == nil {
				arguments = JsonObject{}
			}
			out = append(out, &btypes.ContentBlockMemberToolUse{Value: btypes.ToolUseBlock{
				ToolUseId: aws.String(normalizeBedrockToolID(block.ID)),
				Name:      aws.String(block.Name),
				Input:     bdoc.NewLazyDocument(arguments),
			}})
		case ThinkingContent:
			if strings.TrimSpace(block.Thinking) == "" {
				continue
			}
			if supportsBedrockThinkingSignatureWithName(modelID, modelName) {
				if strings.TrimSpace(block.ThinkingSignature) == "" {
					// Fallback: emit plain text when a
					// thinking block lacks its signature.
					out = append(out, &btypes.ContentBlockMemberText{Value: block.Thinking})
				} else {
					out = append(out, &btypes.ContentBlockMemberReasoningContent{
						Value: &btypes.ReasoningContentBlockMemberReasoningText{
							Value: btypes.ReasoningTextBlock{
								Text:      aws.String(block.Thinking),
								Signature: aws.String(block.ThinkingSignature),
							},
						},
					})
				}
			} else {
				out = append(out, &btypes.ContentBlockMemberReasoningContent{
					Value: &btypes.ReasoningContentBlockMemberReasoningText{
						Value: btypes.ReasoningTextBlock{Text: aws.String(block.Thinking)},
					},
				})
			}
		default:
			continue
		}
	}
	return out, nil
}

// bedrockToolResultRun walks a span of consecutive tool-result messages
// starting at msgs[i] and produces a single content slice combining all
// of them, plus the number of messages consumed.
func bedrockToolResultRun(msgs []Message, i int) ([]btypes.ContentBlock, int, error) {
	out := []btypes.ContentBlock{}
	j := i
	for j < len(msgs) {
		result, ok := msgs[j].(ToolResultMessage)
		if !ok {
			break
		}
		block, err := bedrockToolResultBlock(result)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, block)
		j++
	}
	return out, j - i, nil
}

func bedrockToolResultBlock(message ToolResultMessage) (btypes.ContentBlock, error) {
	var parts []btypes.ToolResultContentBlock
	for _, block := range message.Content {
		switch block := block.(type) {
		case TextContent:
			if text := block.Text; strings.TrimSpace(text) != "" {
				parts = append(parts, &btypes.ToolResultContentBlockMemberText{Value: text})
			}
		case ImageContent:
			image, err := bedrockImageBlock(block.MimeType, block.Data)
			if err != nil {
				return nil, err
			}
			parts = append(parts, &btypes.ToolResultContentBlockMemberImage{Value: image})
		}
	}
	if message.ToolCallID == "" {
		return nil, errors.New("amazon-bedrock: tool result missing toolCallId")
	}
	// Bedrock rejects empty tool-result content; a blank result would orphan
	// the preceding tool_use. Replace with a placeholder.
	if len(parts) == 0 {
		parts = append(parts, &btypes.ToolResultContentBlockMemberText{Value: bedrockEmptyPlaceholder})
	}
	status := btypes.ToolResultStatusSuccess
	if message.IsError {
		status = btypes.ToolResultStatusError
	}
	return &btypes.ContentBlockMemberToolResult{Value: btypes.ToolResultBlock{
		ToolUseId: aws.String(normalizeBedrockToolID(message.ToolCallID)),
		Content:   parts,
		Status:    status,
	}}, nil
}

var bedrockToolIDSanitizeRE = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func normalizeBedrockToolID(id string) string {
	clean := bedrockToolIDSanitizeRE.ReplaceAllString(id, "_")
	if len(clean) > 64 {
		clean = clean[:64]
	}
	return clean
}

func bedrockImageBlock(mime, dataB64 string) (btypes.ImageBlock, error) {
	var format btypes.ImageFormat
	switch mime {
	case "image/jpeg", "image/jpg":
		format = btypes.ImageFormatJpeg
	case "image/png":
		format = btypes.ImageFormatPng
	case "image/gif":
		format = btypes.ImageFormatGif
	case "image/webp":
		format = btypes.ImageFormatWebp
	default:
		return btypes.ImageBlock{}, fmt.Errorf("amazon-bedrock: unknown image type %q", mime)
	}
	bytes, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return btypes.ImageBlock{}, fmt.Errorf("amazon-bedrock: decode image: %w", err)
	}
	return btypes.ImageBlock{
		Format: format,
		Source: &btypes.ImageSourceMemberBytes{Value: bytes},
	}, nil
}

// ─── Tool config ─────────────────────────────────────────────────────────

func convertBedrockTools(tools []ToolSchema, supportsStrictMode bool) (*btypes.ToolConfiguration, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]btypes.Tool, 0, len(tools))
	for _, tool := range tools {
		strict, err := resolveJSONSchemaStrictSampling(tool, supportsStrictMode)
		if err != nil {
			return nil, err
		}
		parameters, err := getJSONSchemaToolParameters(tool, strict)
		if err != nil {
			return nil, err
		}
		if parameters == nil {
			parameters = map[string]any{}
		}
		toolSpec := btypes.ToolSpecification{
			Name:        aws.String(tool.Name),
			Description: aws.String(tool.Description),
			InputSchema: &btypes.ToolInputSchemaMemberJson{Value: bdoc.NewLazyDocument(parameters)},
		}
		if strict != nil && *strict {
			toolSpec.Strict = new(true)
		}
		out = append(out, &btypes.ToolMemberToolSpec{Value: toolSpec})
	}
	return &btypes.ToolConfiguration{
		Tools: out,
		// toolChoice is intentionally left unset (defaults to auto)
		// because wopr's StreamOptions does not currently expose a
		// tool-choice setting. The Provider interface stays small.
	}, nil
}

// ─── Additional model request fields (thinking) ──────────────────────────

func buildBedrockAdditionalFields(model *Model, modelName string, opts StreamOptions) map[string]any {
	if opts.Thinking == "" || opts.Thinking == ThinkingOff || !opts.IsReasoning {
		return nil
	}
	if model == nil || !isAnthropicClaudeBedrockModel(model.ID, modelName) {
		return nil
	}

	display := "summarized"

	if supportsBedrockAdaptiveThinkingWithName(model.ID, modelName) {
		return map[string]any{
			"thinking":      map[string]any{"type": "adaptive", "display": display},
			"output_config": map[string]any{"effort": mapBedrockThinkingEffort(model, opts.Thinking)},
		}
	}

	budget := 0
	if model.Capabilities.MaxOutputTokens > 0 {
		adjustedMax, adjustedBudget := AdjustMaxTokensForThinking(maxTokensPtr(opts.MaxTokens), model.Capabilities.MaxOutputTokens, string(opts.Thinking), nil)
		_ = adjustedMax // The helper owns the budget calculation; Bedrock sends only the thinking budget here.
		budget = adjustedBudget
	} else {
		defaults := map[ThinkingLevel]int{
			ThinkingMinimal: 1024,
			ThinkingLow:     2048,
			ThinkingMedium:  8192,
			ThinkingHigh:    16384,
			ThinkingXHigh:   16384,
			ThinkingMax:     16384,
		}
		budget = defaults[opts.Thinking]
		if budget <= 0 {
			budget = defaults[ThinkingHigh]
		}
	}
	result := map[string]any{
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
			"display":       display,
		},
	}
	// Interleaved thinking defaults on for non-adaptive Claude.
	result["anthropic_beta"] = []string{"interleaved-thinking-2025-05-14"}
	return result
}

// supportsNativeXhighEffort reports whether a Bedrock model supports the
// native "xhigh" effort level. Currently only Claude Opus 4.7+ models.
func supportsNativeXhighEffort(model *Model) bool {
	if model == nil {
		return false
	}
	for _, s := range bedrockModelMatchCandidates(model.ID, model.DisplayName) {
		if strings.Contains(s, "opus-4-7") || strings.Contains(s, "opus-4-8") || strings.Contains(s, "sonnet-5") || strings.Contains(s, "fable-5") {
			return true
		}
	}
	return false
}

func mapBedrockThinkingEffort(model *Model, level ThinkingLevel) string {
	if model != nil {
		if mapped, ok := model.ThinkingLevelMap[level]; ok && mapped != nil {
			return *mapped
		}
	}
	// If level is xhigh and the model supports native xhigh, pass through.
	if level == ThinkingXHigh && supportsNativeXhighEffort(model) {
		return "xhigh"
	}
	switch level {
	case ThinkingMinimal, ThinkingLow:
		return "low"
	case ThinkingMedium:
		return "medium"
	case ThinkingHigh:
		return "high"
	default:
		return "high"
	}
}

// ─── Stream parsing ──────────────────────────────────────────────────────

// activeBlock tracks the streaming state for a single content block
// indexed by Bedrock's contentBlockIndex.
type activeBlock struct {
	kind        string // "text" | "thinking" | "toolUse"
	toolID      string
	toolName    string
	partialJSON strings.Builder
}

type bedrockEventStream interface {
	Events() <-chan btypes.ConverseStreamOutput
	Close() error
	Err() error
}

func (p *BedrockProvider) parseBedrockStream(ctx context.Context, resp *bedrockruntime.ConverseStreamOutput, builder *assistantStreamBuilder) {
	stream := resp.GetStream()
	if stream == nil {
		builder.fail(StopReasonError, errors.New("amazon-bedrock: nil stream"))
		return
	}
	p.parseBedrockEvents(ctx, stream, builder)
}

func (p *BedrockProvider) parseBedrockEvents(ctx context.Context, stream bedrockEventStream, builder *assistantStreamBuilder) {
	defer func() { _ = stream.Close() }()

	blocks := map[int32]*activeBlock{}
	var usage Usage
	stopReason := StopReasonStop
	hasStopReason := false
	var stopErrMsg string

	events := stream.Events()
	for {
		select {
		case <-ctx.Done():
			builder.fail(StopReasonAborted, ctx.Err())
			return
		case ev, ok := <-events:
			if !ok {
				if streamErr := stream.Err(); streamErr != nil {
					if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, io.EOF) {
						// fall through to done
					} else {
						streamErr = mapBedrockTransportError(ctx, streamErr, true)
						builder.fail(StopReasonError, fmt.Errorf("amazon-bedrock: %s", formatBedrockError(streamErr)))
						return
					}
				}
				if !hasStopReason {
					builder.fail(StopReasonError, errors.New("amazon-bedrock: Bedrock stream ended without a stop reason"))
					return
				}
				if stopReason == StopReasonError {
					message := stopErrMsg
					if message == "" {
						message = "An unknown error occurred"
					}
					builder.fail(StopReasonError, fmt.Errorf("amazon-bedrock: %s", message))
					return
				}
				builder.done(stopReason, &usage, "")
				return
			}
			switch e := ev.(type) {
			case *btypes.ConverseStreamOutputMemberMessageStart:
				if e.Value.Role != btypes.ConversationRoleAssistant {
					builder.fail(StopReasonError, errors.New("amazon-bedrock: Unexpected assistant message start but got user message start instead"))
					return
				}
				builder.start()

			case *btypes.ConverseStreamOutputMemberContentBlockStart:
				idx := aws.ToInt32(e.Value.ContentBlockIndex)
				if start := e.Value.Start; start != nil {
					if tu, ok := start.(*btypes.ContentBlockStartMemberToolUse); ok {
						blocks[idx] = &activeBlock{
							kind:     "toolUse",
							toolID:   aws.ToString(tu.Value.ToolUseId),
							toolName: aws.ToString(tu.Value.Name),
						}
						builder.toolCallStart(streamToolCallDelta{
							index: int(idx), id: aws.ToString(tu.Value.ToolUseId), name: aws.ToString(tu.Value.Name),
						})
					}
				}

			case *btypes.ConverseStreamOutputMemberContentBlockDelta:
				p.handleBedrockDelta(e.Value, blocks, builder)

			case *btypes.ConverseStreamOutputMemberContentBlockStop:
				idx := aws.ToInt32(e.Value.ContentBlockIndex)
				if block, ok := blocks[idx]; ok {
					switch block.kind {
					case "text":
						builder.endText()
					case "thinking":
						builder.endThinking()
					case "toolUse":
						builder.endToolCall(int(idx))
					}
				}
				delete(blocks, idx)

			case *btypes.ConverseStreamOutputMemberMessageStop:
				rawStopReason := string(e.Value.StopReason)
				builder.setResponseMetadata("", "", rawStopReason, "", nil)
				stopReason, stopErrMsg = mapBedrockStopReason(rawStopReason)
				hasStopReason = true

			case *btypes.ConverseStreamOutputMemberMetadata:
				if u := e.Value.Usage; u != nil {
					handleBedrockUsage(u, builder, &usage)
				}
			}
		}
	}
}

// handleBedrockUsage records metadata usage: 1h cache writes are summed from cacheDetails and the usage is priced.
func handleBedrockUsage(u *btypes.TokenUsage, builder *assistantStreamBuilder, usage *Usage) {
	usage.Input = int(aws.ToInt32(u.InputTokens))
	usage.Output = int(aws.ToInt32(u.OutputTokens))
	usage.CacheRead = int(aws.ToInt32(u.CacheReadInputTokens))
	usage.CacheWrite = int(aws.ToInt32(u.CacheWriteInputTokens))
	usage.CacheWrite1h = nil
	if u.CacheDetails != nil {
		longWrite := 0
		for _, detail := range u.CacheDetails {
			if detail.Ttl == btypes.CacheTTLOneHour {
				longWrite += int(aws.ToInt32(detail.InputTokens))
			}
		}
		usage.CacheWrite1h = &longWrite
	}
	usage.TotalTokens = int(aws.ToInt32(u.TotalTokens))
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.Input + usage.Output
	}
	builder.calculateCost(usage)
	builder.setUsage(usage)
}

func (p *BedrockProvider) handleBedrockDelta(ev btypes.ContentBlockDeltaEvent, blocks map[int32]*activeBlock, builder *assistantStreamBuilder) {
	idx := aws.ToInt32(ev.ContentBlockIndex)
	switch d := ev.Delta.(type) {
	case *btypes.ContentBlockDeltaMemberText:
		// Lazily create a text block if needed (Bedrock does not emit
		// ContentBlockStart for text blocks).
		if _, ok := blocks[idx]; !ok {
			blocks[idx] = &activeBlock{kind: "text"}
		}
		builder.textDelta(d.Value)

	case *btypes.ContentBlockDeltaMemberToolUse:
		b, ok := blocks[idx]
		if !ok {
			return
		}
		b.partialJSON.WriteString(aws.ToString(d.Value.Input))
		builder.toolCallDelta(streamToolCallDelta{
			index: int(idx), id: b.toolID, name: b.toolName, argumentsDelta: aws.ToString(d.Value.Input),
		})

	case *btypes.ContentBlockDeltaMemberReasoningContent:
		switch rc := d.Value.(type) {
		case *btypes.ReasoningContentBlockDeltaMemberText:
			if _, ok := blocks[idx]; !ok {
				blocks[idx] = &activeBlock{kind: "thinking"}
			}
			builder.thinkingDelta(rc.Value, false)
		case *btypes.ReasoningContentBlockDeltaMemberSignature:
			builder.thinkingSignature(rc.Value)
		}
	}
}

func mapBedrockStopReason(reason string) (StopReason, string) {
	switch reason {
	case string(btypes.StopReasonEndTurn), string(btypes.StopReasonStopSequence):
		return StopReasonStop, ""
	case string(btypes.StopReasonMaxTokens), string(btypes.StopReasonModelContextWindowExceeded):
		return StopReasonLength, ""
	case string(btypes.StopReasonToolUse):
		return StopReasonToolUse, ""
	default:
		return StopReasonError, reason
	}
}

// ─── Error formatting ────────────────────────────────────────────────────

// bedrockErrorPrefixes maps Bedrock error names to message prefixes. Retry classifiers downstream match patterns like
// "service.?unavailable" against these prefixes.
var bedrockErrorPrefixes = map[string]string{
	"InternalServerException":     "Internal server error",
	"ModelStreamErrorException":   "Model stream error",
	"ValidationException":         "Validation error",
	"ThrottlingException":         "Throttling error",
	"ServiceUnavailableException": "Service unavailable",
}

func formatBedrockError(err error) string {
	if err == nil {
		return ""
	}
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		prefix, ok := bedrockErrorPrefixes[apiErr.ErrorCode()]
		if !ok {
			prefix = apiErr.ErrorCode()
		}
		msg := apiErr.ErrorMessage()
		return fmt.Sprintf("%s: %s%s", prefix, msg, bedrockDataRetentionHint(msg))
	}
	return err.Error() + bedrockDataRetentionHint(err.Error())
}

// bedrockDataRetentionHint appends a link to the AWS data-retention docs when a
// Bedrock error indicates the model rejected the configured data retention
// mode.
func bedrockDataRetentionHint(message string) string {
	if strings.Contains(strings.ToLower(message), "data retention mode") {
		return " See https://docs.aws.amazon.com/bedrock/latest/userguide/data-retention.html for supported data retention modes."
	}
	return ""
}
