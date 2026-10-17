package autonomy

import (
	ctxsvc "github.com/kaulie/autonomy/src/context"
	"github.com/kaulie/autonomy/src/knowledgebase"
)

// buildContextService wires agent context.* onto knowledge-base. Register,
// bind and sync live on that service; this runtime only reads
// (KNOWLEDGE_BASE_URL, default 49.234.45.173:4360).
func buildContextService() ctxsvc.Service {
	return knowledgebase.New()
}
