package changes

import (
	"fmt"
	"strings"
	"testing"
)

// The one property everything else here is for: whatever the input, no value
// reaches the description. Values are chosen to be unmistakable, and checked
// for both before and after, because a removed password is as secret as a new
// one.
func TestNoValueEverReachesTheDescription(t *testing.T) {
	secret := map[string]string{"DB_PASSWORD": "hunter2-old", "API_TOKEN": "tok-kept"}
	edited := map[string]string{"DB_PASSWORD": "hunter2-new", "API_TOKEN": "tok-kept", "MAIL_PASS": "m4il-added"}

	yamlBefore := "datasource:\n  password: yaml-old-pw\n  pool:\n    max: 20\n"
	yamlAfter := "datasource:\n  password: yaml-new-pw\n  pool:\n    max: 40\nmail:\n  pass: yaml-added-pw\n"

	propsBefore := "db.password=props-old-pw\n"
	propsAfter := "db.password=props-new-pw\nmail.pass=props-added-pw\n"

	descriptions := []string{
		Keys(secret, edited),
		Keys(nil, edited),
		Keys(secret, nil),
		Document("application.yml", yamlBefore, yamlAfter, true),
		Document("application.yml", "", yamlAfter, false),
		Document("app.properties", propsBefore, propsAfter, true),
		Document("logback.xml", "<pw>xml-old-pw</pw>", "<pw>xml-new-pw</pw>", true),
		Document("broken.yaml", "a: [unclosed-old-pw", "a: [unclosed-new-pw", true),
	}
	values := []string{
		"hunter2", "tok-kept", "m4il-added",
		"yaml-old-pw", "yaml-new-pw", "yaml-added-pw", "20", "40",
		"props-old-pw", "props-new-pw", "props-added-pw",
		"xml-old-pw", "xml-new-pw", "unclosed",
	}
	for _, d := range descriptions {
		for _, v := range values {
			if strings.Contains(d, v) {
				t.Errorf("%q contains the value %q", d, v)
			}
		}
	}
}

func TestKeysNamesWhatWasAddedChangedAndRemoved(t *testing.T) {
	got := Keys(
		map[string]string{"KEEP": "same", "EDIT": "old", "DROP": "x"},
		map[string]string{"KEEP": "same", "EDIT": "new", "NEW": "y"},
	)
	want := "added NEW; changed EDIT; removed DROP"
	if got != want {
		t.Errorf("Keys() = %q, want %q", got, want)
	}
}

// A save that changed nothing says so, rather than saying nothing: "saved,
// identical" is itself an answer to a review.
func TestAnUnchangedSaveSaysSo(t *testing.T) {
	same := map[string]string{"A": "1"}
	if got := Keys(same, same); got != NoChange {
		t.Errorf("Keys() = %q, want %q", got, NoChange)
	}
	if got := Document("a.yaml", "a: 1\n", "a: 1", true); got != NoChange {
		t.Errorf("a final newline is not a change: got %q", got)
	}
}

func TestYAMLIsDescribedSettingBySetting(t *testing.T) {
	before := "server:\n  port: 8080\ndatasource:\n  pool:\n    max: 20\n  url: jdbc:x\n"
	after := "server:\n  port: 8080\ndatasource:\n  pool:\n    max: 40\nredis:\n  timeout: 5s\n"
	got := Document("application.yml", before, after, true)
	want := "added redis.timeout; changed datasource.pool.max; removed datasource.url"
	if got != want {
		t.Errorf("Document() = %q, want %q", got, want)
	}
}

// Inserting at the front of a list must read as one change, not as every
// element after it changing.
func TestAListIsOneSetting(t *testing.T) {
	before := "profiles: [a, b, c]\n"
	after := "profiles: [z, a, b, c]\n"
	if got := Document("x.yaml", before, after, true); got != "changed profiles" {
		t.Errorf("Document() = %q, want %q", got, "changed profiles")
	}
}

// "1" and 1 are different settings to whatever reads the file.
func TestATypeChangeIsAChange(t *testing.T) {
	if got := Document("x.yaml", "port: 8080\n", "port: \"8080\"\n", true); got != "changed port" {
		t.Errorf("Document() = %q, want %q", got, "changed port")
	}
}

func TestSeveralDocumentsAreToldApart(t *testing.T) {
	before := "a: 1\n---\nb: 1\n"
	after := "a: 1\n---\nb: 2\n"
	if got := Document("x.yaml", before, after, true); got != "changed [1].b" {
		t.Errorf("Document() = %q, want %q", got, "changed [1].b")
	}
}

func TestJSONIsReadAsYAML(t *testing.T) {
	got := Document("x.json", `{"a":{"b":1}}`, `{"a":{"b":2},"c":true}`, true)
	if got != "added c; changed a.b" {
		t.Errorf("Document() = %q", got)
	}
}

func TestANewFileSaysSo(t *testing.T) {
	got := Document("x.yaml", "", "a: 1\nb: 2\n", false)
	if got != "new file; added a, b" {
		t.Errorf("Document() = %q", got)
	}
}

func TestPropertiesAreComparedKeyByKey(t *testing.T) {
	before := "# a comment\ndb.url=jdbc:a\nlong.value=first \\\n  second\nmode: fast\n"
	after := "db.url=jdbc:b\nlong.value=first \\\n  second\nmode: fast\nnew.key value\n"
	if got := Document("app.properties", before, after, true); got != "added new.key; changed db.url" {
		t.Errorf("Document() = %q", got)
	}
}

func TestAnythingElseIsCountedInLines(t *testing.T) {
	before := "<a/>\n<b/>\n<c/>\n"
	after := "<a/>\n<B/>\n<c/>\n<d/>\n"
	if got := Document("logback.xml", before, after, true); got != "2 lines added, 1 removed" {
		t.Errorf("Document() = %q", got)
	}
}

// Broken YAML is still a change worth describing; a parse error must not be
// the reason a record is empty.
func TestYAMLThatDoesNotParseFallsBackToLines(t *testing.T) {
	got := Document("x.yaml", "a: [1, 2\n", "a: [1, 2, 3\n", true)
	if got != "1 line added, 1 removed" {
		t.Errorf("Document() = %q", got)
	}
}

// A file rewritten from scratch must not produce a record as long as the file.
func TestLongListsAreCut(t *testing.T) {
	after := map[string]string{}
	for i := 0; i < 25; i++ {
		after[fmt.Sprintf("K%02d", i)] = "v"
	}
	got := Keys(nil, after)
	if !strings.HasSuffix(got, " and 15 more") || strings.Count(got, ",") != maxNames-1 {
		t.Errorf("Keys() = %q", got)
	}
}
