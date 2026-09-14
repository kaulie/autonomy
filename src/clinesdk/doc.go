// Package clinesdk is the Go client for the autonomy cline bridge: it spawns
// one Node process running `bridge/bridge.mjs`, which wraps the Cline SDK
// (@cline/sdk) and exposes resident agent sessions over stdio NDJSON.
//
// Why a bridge: the Cline agent core is TypeScript/Node only while the autonomy
// runtime is Go. The bridge owns the resident Cline session (one per autonomy
// Agent, surviving many prompts) and streams the SDK's native events; this
// package owns process lifecycle, request/response correlation and the event
// fan-out, and deliberately does not interpret provider payloads — mapping them
// onto the neutral LLMEvent model is the autonomy layer's job
// (src/llm_event_cline.go).
//
// Protocol (one JSON object per line, both directions):
//
//	bridge -> runtime
//	  {"type":"ready","protocol":"cline-bridge/1","pid":1,"node":"v22","sdk":"0.0.82"}
//	  {"type":"event","requestId":"req-1","agentId":"cls_..","sessionId":"cls-..","event":{...}}
//	  {"type":"result","id":"req-1","ok":true,"result":{...}}
//	  {"type":"result","id":"req-1","ok":false,"error":{"code":"..","message":".."}}
//	runtime -> bridge
//	  {"id":"req-1","cmd":"createAgent","params":{...}}
//
// Commands: ping | models | createAgent | send | stop | close | usage | shutdown
//
// Configuration (environment):
//
//	AUTONOMY_CLINE_NODE_BIN      node executable (default "node")
//	AUTONOMY_CLINE_BRIDGE_SCRIPT path to bridge.mjs (default: the script shipped
//	                             under src/clinesdk/bridge/, or $PROJECT_ROOT/...)
//	AUTONOMY_CLINE_PROVIDER      Cline provider id, e.g. "deepseek", "anthropic"
//	AUTONOMY_CLINE_MODEL         model id, e.g. "deepseek-v4-pro"
//	AUTONOMY_CLINE_API_KEY       provider API key (optional: the bridge also sees
//	                             the credentials saved by `cline auth`)
//	AUTONOMY_CLINE_BASE_URL      provider base URL override, when needed
package clinesdk
