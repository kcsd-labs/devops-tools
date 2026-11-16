package kube

import "testing"

func TestRequiredValues(t *testing.T) {
	cases := []struct {
		msg  string
		want []string
	}{
		{`execution error at (gitlab/charts/certmanager-issuer/templates/configmap.yaml:21:8): You must provide an email to associate with your TLS certificates. Please set certmanager-issuer.email`,
			[]string{"certmanager-issuer.email"}},
		{`execution error at (app/templates/secret.yaml:3:5): A value for auth.password is required`, []string{"auth.password"}},
		{`execution error at (app/templates/x.yaml:1:1): global.domain is required`, []string{"global.domain"}},
		{"values don't meet the specifications of the schema(s) in the following chart(s):\napp:\n- at '/auth': missing property 'password'\n- at '': missing properties 'host', 'port'",
			[]string{"auth.password", "host", "port"}},
		{"values don't meet the specifications of the schema(s) in the following chart(s):\nsub:\n- at '/db': missing property 'name'",
			[]string{"sub.db.name"}},
		{"values don't meet the specifications of the schema(s) in the following chart(s):\napp:\n- (root): token is required\n- auth: user is required",
			[]string{"token", "auth.user"}},
		{`execution error at (app/templates/x.yaml:1:1): something else went wrong`, nil},
	}
	for _, c := range cases {
		got := requiredValues(c.msg, "app")
		if c.msg[:9] == "execution" && len(c.want) > 0 && c.want[0] == "certmanager-issuer.email" {
			got = requiredValues(c.msg, "gitlab")
		}
		var keys []string
		for _, r := range got {
			keys = append(keys, r.Key)
		}
		if len(keys) != len(c.want) {
			t.Errorf("%q\n got %v, want %v", c.msg, keys, c.want)
			continue
		}
		for i := range keys {
			if keys[i] != c.want[i] {
				t.Errorf("%q\n got %v, want %v", c.msg, keys, c.want)
			}
		}
	}
	got := requiredValues(cases[0].msg, "gitlab")
	if len(got) != 1 || got[0].Said != "You must provide an email to associate with your TLS certificates. Please set certmanager-issuer.email" {
		t.Errorf("said = %+v", got)
	}
}
