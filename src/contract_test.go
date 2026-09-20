package autonomy

import (
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// The service contract is generated from the swag annotations (scripts/register-contract.sh
// → swag init → the service registry), and those annotations live next to the handlers
// in src/http_server.go. That makes them the single source of truth — which only holds if
// they agree with what is really served: an endpoint without an annotation, or an
// annotation naming a path nobody serves, is a contract that lies about this runtime.
func TestSwagAnnotationsMatchTheRoutes(t *testing.T) {
	annotated := annotatedRoutes(t, "http_server.go")
	served := servedRoutes()
	sort.Strings(annotated)
	sort.Strings(served)
	if strings.Join(annotated, "\n") != strings.Join(served, "\n") {
		t.Errorf("swag 注解与路由表不一致（注解是契约的唯一真源，两边必须一一对应）\n注解 %d 条:\n%s\n路由 %d 条:\n%s",
			len(annotated), strings.Join(annotated, "\n"), len(served), strings.Join(served, "\n"))
	}
}

// annotatedRoutes reads every `@Router <path> [method]` out of one source file.
func annotatedRoutes(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
	var routes []string
	for _, group := range file.Comments {
		for _, comment := range group.List {
			line := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
			if !strings.HasPrefix(line, "@Router") {
				continue
			}
			// 期望格式：@Router /api/tasks/{taskID} [get]
			fields := strings.Fields(line)
			if len(fields) != 3 {
				t.Errorf("无法解析注解 %q（期望 @Router <path> [method]）", comment.Text)
				continue
			}
			routes = append(routes, strings.ToUpper(strings.Trim(fields[2], "[]"))+" "+fields[1])
		}
	}
	if len(routes) == 0 {
		t.Fatalf("%s 里没有 @Router 注解：注解没了，登记出去的契约就只剩 /health", path)
	}
	return routes
}

// servedRoutes is what the mux is actually given: the route table, as the contract
// spells it (METHOD + path).
func servedRoutes() []string {
	server := NewHTTPServer(nil)
	routes := make([]string, 0, len(server.routes()))
	for _, route := range server.routes() {
		method, path, ok := strings.Cut(route.Pattern, " ")
		if !ok {
			continue
		}
		routes = append(routes, strings.ToUpper(method)+" "+path)
	}
	return routes
}
