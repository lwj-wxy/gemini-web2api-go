package app

import (
	"context"
	"fmt"
)

func mediaRequestContext(contexts []context.Context) context.Context {
	if len(contexts) > 0 && contexts[0] != nil {
		return contexts[0]
	}
	return context.Background()
}

// Recovery reads the existing response/conversation and never submits generation.
func recoverImageResponse(ctx context.Context, raw []byte, cookie, sapisid, xsrf, proxyURL string) (*StreamResult, error) {
	if len(collectImageURLs(string(raw))) == 0 && extractConversationID(string(raw)) == "" {
		return nil, fmt.Errorf("no conversation metadata received; generation outcome is unconfirmed")
	}
	arts, err := fetchImageArtifacts(string(raw), extractConversationID(string(raw)), cookie, sapisid, xsrf, proxyURL, "image/png", ctx)
	if err != nil {
		return nil, err
	}
	return &StreamResult{Raw: string(raw), Artifacts: arts, UpstreamModel: extractUpstreamModel(string(raw))}, nil
}
