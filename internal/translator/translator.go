package translator

import (
	"github.com/cnf/arbiter/pkg/types"
)

// Translator converts between Anthropic and OpenAI formats bidirectionally.
type Translator interface {
	ToOpenAI(req *types.AnthropicRequest) (*types.OpenAIRequest, error)
	FromOpenAI(resp *types.OpenAIResponse) (*types.AnthropicResponse, error)
	ToAnthropic(req *types.OpenAIRequest) (*types.AnthropicRequest, error)
	FromAnthropic(resp *types.AnthropicResponse) (*types.OpenAIResponse, error)
}

// Normalizer detects request format and converts to NormalizedRequest.
type Normalizer interface {
	Detect(payload []byte) (string, error) // returns "anthropic" or "openai"
	ToNormalized(payload []byte, format string) (*types.NormalizedRequest, error)
}

// Denormalizer converts NormalizedResponse back to original format.
type Denormalizer interface {
	FromNormalized(resp *types.NormalizedResponse, originalFormat string) (interface{}, error)
}

// DefaultTranslator implements Translator.
type DefaultTranslator struct{}

// NewDefaultTranslator creates a translator.
func NewDefaultTranslator() *DefaultTranslator {
	return &DefaultTranslator{}
}

func (dt *DefaultTranslator) ToOpenAI(req *types.AnthropicRequest) (*types.OpenAIRequest, error) {
	// TODO: implement Anthropic -> OpenAI conversion
	return &types.OpenAIRequest{}, nil
}

func (dt *DefaultTranslator) FromOpenAI(resp *types.OpenAIResponse) (*types.AnthropicResponse, error) {
	// TODO: implement OpenAI -> Anthropic response conversion
	return &types.AnthropicResponse{}, nil
}

func (dt *DefaultTranslator) ToAnthropic(req *types.OpenAIRequest) (*types.AnthropicRequest, error) {
	// TODO: implement OpenAI -> Anthropic conversion
	return &types.AnthropicRequest{}, nil
}

func (dt *DefaultTranslator) FromAnthropic(resp *types.AnthropicResponse) (*types.OpenAIResponse, error) {
	// TODO: implement Anthropic -> OpenAI response conversion
	return &types.OpenAIResponse{}, nil
}
