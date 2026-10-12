package autonomy

import (
	ctxsvc "github.com/kaulie/autonomy/src/context"
	"github.com/kaulie/autonomy/src/knowledgebase"
)

// buildContextService wires agent context.* onto knowledge-base. Register,
// bind and sync live on that service; this runtime only reads
// (KNOWLEDGE_BASE_URL, default 127.0.0.1:4360).
func buildContextService() ctxsvc.Service {
	return knowledgebase.New()
}
