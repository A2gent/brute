package agent

import (
	"unicode/utf8"

	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

// Read-cache repair can expand a reference after transcript admission. Enforce
// the same invariant on expanded content before it is saved or sent again.
func (a *Agent) admitExpandedRequestResults(sess *session.Session, request *llm.ChatRequest) {
	for mi := range request.Messages {
		for ri := range request.Messages[mi].ToolResults {
			tr := request.Messages[mi].ToolResults[ri]
			if (utf8.RuneCountInString(tr.Content)+3)/4 <= a.config.ToolResultMaxTokens {
				continue
			}
			admitted := a.compressor.AdmitToolResult(sess, tr, a.config.ToolResultMaxTokens)
			request.Messages[mi].ToolResults[ri] = admitted
			contextcompress.EnableRetrieval(request)
			for si := range sess.Messages {
				for ti := range sess.Messages[si].ToolResults {
					target := &sess.Messages[si].ToolResults[ti]
					if target.ToolCallID == tr.ToolCallID {
						target.Content = admitted.Content
						target.Metadata = admitted.Metadata
					}
				}
			}
		}
	}
}
