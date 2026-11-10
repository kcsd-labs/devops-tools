package shell

import (
	"strings"
	"testing"
)

func TestOnlyPlainTextLinesPass(t *testing.T) {
	for _, ok := range []string{"", "ls -la /app", "echo 'привет'", `psql -c "select 1;"`} {
		if err := CheckLine(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	// Each of these would make the shell run something other than the text.
	for _, bad := range []string{"ls\t", "\x1b[A", "rm x\x15ls", "a\nb", "a\rb", "\x03", "x\x7f"} {
		if err := CheckLine(bad); err != ErrLineControl {
			t.Errorf("%q: err = %v, want a refusal", bad, err)
		}
	}
	if err := CheckLine(strings.Repeat("a", MaxLine+1)); err != ErrLineTooLong {
		t.Errorf("long line: %v", err)
	}
	if err := CheckLine("\xff"); err != ErrLineUTF8 {
		t.Errorf("bad utf-8: %v", err)
	}
}

func TestCredentialsInCommandsAreMasked(t *testing.T) {
	for in, want := range map[string]string{
		`PGPASSWORD=hunter2 psql -h db`:                     `PGPASSWORD=*** psql -h db`,
		`curl -H "Authorization: Bearer abc.def" http://x/`: `curl -H "Authorization: Bearer ***" http://x/`,
		`mysql -u root -pS3cret app`:                        `mysql -u root -p*** app`,
		`app --password 's p' --token=xyz`:                  `app --password *** --token=***`,
		`psql postgres://app:hunter2@db:5432/app`:           `psql postgres://app:***@db:5432/app`,
		`export API_KEY="k-123"`:                            `export API_KEY=***`,
	} {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	// Flags that merely look alike are left as they are.
	for _, keep := range []string{"mkdir -p /tmp/x", "ssh -p 22 host", "ls -la", "cat /etc/passwd"} {
		if got := Mask(keep); got != keep {
			t.Errorf("Mask(%q) = %q, want it untouched", keep, got)
		}
	}
}
