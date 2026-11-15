package main

import "testing"

func TestClusterCapable(t *testing.T) {
	cases := []struct {
		script, interp string
		want           bool
	}{
		{"app.js", "", true},
		{"src/index.ts", "", true},
		{"app.ts", "bun", true},
		{"app.js", "node", true},
		{"/home/u/.nvm/versions/node/v24/bin/bun", "", true}, // pm0 start bun -i 2 -- run x.ts
		{"bun", "", true},
		{"node", "", true},
		{"app.py", "", false},
		{"app.js", "python3", false},
		{"./server", "", false},
		{"/usr/local/bin/bun", "none", true}, // deploy-engine shape: script=bun, interpreter=none
		{"app.js", "none", false},            // direct exec of a non-runtime script
	}
	for _, tc := range cases {
		if got := clusterCapable(tc.script, tc.interp); got != tc.want {
			t.Errorf("clusterCapable(%q, %q) = %v, want %v", tc.script, tc.interp, got, tc.want)
		}
	}
}
