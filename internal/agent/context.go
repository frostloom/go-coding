package agent

import (
	"context"

	"github.com/zlx05/go-coding/internal/codeindex"
)

// ContextProvider builds and incrementally refreshes the symbol index for
// the sandbox repo, then retrieves task-relevant context for prompts.
// It is symbol-only (precise + graph): no embeddings, no external services,
// deterministic and cheap — matching how Claude Code gathers context via
// search and reading rather than vector retrieval.
type ContextProvider struct {
	builder     *codeindex.Builder
	retriever   *codeindex.Retriever
	maxResults  int
	tokenBudget int
}

// NewContextProvider indexes repoPath once. Failure to index (e.g. non-Go
// repo) is non-fatal: the provider yields empty context and the agent
// falls back to the whole-file snapshot in the prompt.
func NewContextProvider(repoPath string) *ContextProvider {
	return NewContextProviderWithBuilder(repoPath, codeindex.NewBuilder(repoPath))
}

// NewContextProviderWithBuilder builds the provider on a shared builder.
// Sharing one builder between retrieval and the EditTool's edit-time symbol
// validation keeps them on the same index: a rejected edit rolls the index
// back, and a successful edit immediately refreshes retrieval context.
func NewContextProviderWithBuilder(repoPath string, b *codeindex.Builder) *ContextProvider {
	if _, err := b.Build(); err != nil {
		// Non-fatal: retrieval will just be empty.
		return &ContextProvider{}
	}
	return &ContextProvider{
		builder:     b,
		retriever:   codeindex.NewRetriever(b.Index(), repoPath),
		maxResults:  10,
		tokenBudget: 3000,
	}
}

// Refresh re-parses changed files (incremental). Returns re-parsed count.
func (cp *ContextProvider) Refresh() int {
	if cp.builder == nil {
		return 0
	}
	n, err := cp.builder.Build()
	if err != nil {
		return 0
	}
	return n
}

// Context retrieves symbols relevant to a query and renders them as text.
func (cp *ContextProvider) Context(ctx context.Context, query string) string {
	if cp.retriever == nil {
		return ""
	}
	return cp.retriever.Render(ctx, query, cp.maxResults, cp.tokenBudget)
}

// FileList returns the sorted .go files in the repo for the prompt.
func (cp *ContextProvider) FileList() []string {
	if cp.builder == nil {
		return nil
	}
	return cp.builder.Index().SortedFiles()
}
