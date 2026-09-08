package cursorsdk

import (
	"fmt"
	"testing"
)

func TestIsNotFound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("other"), false},
		{&NotFoundError{RpcError: RpcError{Code: "not_found", Message: "gone"}}, true},
		{&RpcError{Code: "internal", Message: "Agent xyz not found"}, true},
		{fmt.Errorf("not_found: Agent missing"), true},
	}
	for _, tc := range cases {
		if got := IsNotFound(tc.err); got != tc.want {
			t.Fatalf("IsNotFound(%v)=%v want %v", tc.err, got, tc.want)
		}
	}
}
