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

func TestALocaleOtherThanUTF8IsConverted(t *testing.T) {
	for _, none := range []string{"", "UTF-8", "utf8", "ANSI_X3.4-1968"} {
		if LookupCharset(none) != nil {
			t.Errorf("%q should need no conversion", none)
		}
	}
	c := LookupCharset("CP1251")
	if c == nil {
		t.Fatal("CP1251 should be known")
	}
	// "команда не найдена" as bash prints it under ru_RU.CP1251.
	cp := []byte{0xea, 0xee, 0xec, 0xe0, 0xed, 0xe4, 0xe0}
	got, err := c.Decoder().Bytes(cp)
	if err != nil || string(got) != "команда" {
		t.Errorf("decoded %q, %v", got, err)
	}
	enc, err := c.Encode("echo привет")
	if err != nil || enc != "echo \xef\xf0\xe8\xe2\xe5\xf2" {
		t.Errorf("encoded %q, %v", enc, err)
	}
	if _, err := c.Encode("echo 😀"); err == nil {
		t.Error("a character CP1251 has no place for should be refused, not replaced")
	}
	for _, name := range []string{"KOI8-R", "ISO-8859-5"} {
		if LookupCharset(name) == nil {
			t.Errorf("%s should be known", name)
		}
	}
}
