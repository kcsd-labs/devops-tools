package kube

import (
	"reflect"
	"strings"
	"testing"
)

func TestConfigMapYAMLRoundTrips(t *testing.T) {
	data := map[string]string{
		"prometheus.yml":            "global:\n  scrape_interval: 10s\nrule_files:\n- /etc/config/rules\n",
		"no-trailing-newline.sh":    "echo one\necho two",
		"allow-snippet-annotations": "false",
		"RETRIES":                   "3",
		"EMPTY":                     "",
		"QUOTED":                    `say "hi": now`,
		"rules":                     "{}\n",
	}
	text := RenderConfigMapData(data)
	if !strings.Contains(text, "prometheus.yml: |\n    global:\n") {
		t.Errorf("a file is not a literal block:\n%s", text)
	}
	if !strings.Contains(text, `allow-snippet-annotations: "false"`) {
		t.Errorf("a string that looks like a boolean is not quoted:\n%s", text)
	}
	back, err := ParseConfigMapData(text)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, data) {
		t.Errorf("round trip changed the values:\n got %#v\nwant %#v", back, data)
	}
}

func TestAnEmptyConfigMapRendersAsAnEmptyBlock(t *testing.T) {
	if got := RenderConfigMapData(map[string]string{}); got != "data: {}\n" {
		t.Errorf("got %q", got)
	}
	for _, in := range []string{"", "data:\n", "data: {}\n"} {
		if got, err := ParseConfigMapData(in); err != nil || len(got) != 0 {
			t.Errorf("%q: %v, %v", in, got, err)
		}
	}
}

func TestConfigMapYAMLAsPeopleWriteIt(t *testing.T) {
	// A manifest pasted from kubectl, unquoted numbers and booleans, an empty value.
	in := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  RETRIES: 3\n  ENABLED: true\n  BLANK:\n"
	got, err := ParseConfigMapData(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"RETRIES": "3", "ENABLED": "true", "BLANK": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func TestConfigMapYAMLThatCannotBeSavedIsRefusedPlainly(t *testing.T) {
	for in, want := range map[string]string{
		"data:\n  a: [1, 2]\n":        "not text",
		"data:\n  a:\n    b: c\n":     "not text",
		"data: [a]\n":                 "list of keys",
		"data:\n  a: 1\nspec: {}\n":   "only data",
		"binaryData:\n  a: AA==\n":    "binary",
		"data:\n  a: 1\n  a: 2\n":     "",
		"data:\n  a: 'unterminated\n": "does not parse",
	} {
		_, err := ParseConfigMapData(in)
		if err == nil || (want != "" && !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: err = %v, want one saying %q", in, err, want)
		}
	}
}
