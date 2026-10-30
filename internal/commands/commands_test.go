// SPDX-License-Identifier: MIT

package commands

import (
	"strings"
	"testing"
)

// TestUnknownCommand checks the errors Run returns for unknown commands,
// which main prints after "defy-installer: ".
func TestUnknownCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"nuron"}, `unknown command "nuron"; see defy-installer --help`},
		{[]string{"help", "nuron"}, `unknown command "nuron"; see defy-installer --help`},
		{[]string{"advanced", "neuron", "wirte"}, `unknown command "wirte"; see defy-installer advanced neuron --help`},
		{[]string{"adv", "halves", "--help", "wirte"}, `unknown command "wirte"; see defy-installer advanced halves --help`},
		{[]string{"debug", "prots"}, `unknown command "prots"; see defy-installer debug --help`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errs strings.Builder
			root := testDeps(t).rootCommand()
			root.Writer, root.ErrWriter = &out, &errs
			err := runRoot(t.Context(), root, append([]string{"defy-installer"}, tc.args...))
			if err == nil || err.Error() != tc.want || out.Len() > 0 || errs.Len() > 0 {
				t.Errorf("error %v, stdout %q, stderr %q; want %q only", err, out.String(), errs.String(), tc.want)
			}
		})
	}
}
