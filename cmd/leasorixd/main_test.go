package main

import "testing"

func TestRunCommands(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"version"}, 0},
		{[]string{"--version"}, 0},
		{[]string{"help"}, 0},
		{[]string{"bogus"}, 2},              // must not start the server
		{[]string{"--config", "x", "y"}, 2}, // stray argument after flags
		{[]string{"ha"}, 2},
	} {
		if got := run(tc.args); got != tc.want {
			t.Errorf("run(%q) = %d, want %d", tc.args, got, tc.want)
		}
	}
}
