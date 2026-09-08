{{- define "devops-tools.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "devops-tools.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "devops-tools.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "devops-tools.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "devops-tools.selectorLabels" -}}
app.kubernetes.io/name: {{ include "devops-tools.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "devops-tools.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "devops-tools.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Where auth.ldap.tls.caCert is mounted when it is given in values.

Named in one place because two templates have to agree on it: the deployment
mounts the file here, and the ConfigMap points caFile at it. Disagreeing would
produce a portal that starts and then fails every sign-in with "no such file".
*/}}
{{- define "devops-tools.ldapCADir" -}}/etc/devops-tools/ldap-ca{{- end -}}
{{- define "devops-tools.ldapCAPath" -}}{{ include "devops-tools.ldapCADir" . }}/ca.crt{{- end -}}

{{/*
Every credential this chart can carry, and where each one comes from.

Two ways to supply any of them, and they are mutually exclusive:

  the value itself      written in values, beside the setting it belongs to
  <field>Secret.name    a Secret you already have; .key says which entry in it,
                        and defaults to the variable the value arrives as

Either way the service reads them from the environment and nowhere else. The
configuration file becomes a ConfigMap, and read access to one of those is
handed out far more freely than to a Secret — so a value written in values is
moved into the chart's own Secret, and a named Secret is referenced where it
already stands.

One place, because three templates have to agree: the Secret writes the inline
ones, the deployment names all of them, and the ConfigMap removes every trace.
Disagreeing quietly would mean a credential in a ConfigMap.

Each entry answers three things: which variable it arrives as (empty for the
CA certificate, which is a file rather than a variable), the inline value if
there is one, and the Secret and key to point at if there is not.
*/}}
{{- define "devops-tools.credentials" -}}
{{- $c := .Values.config -}}
{{- $specs := list
      (dict "id" "bootstrapPassword" "env" "DEVOPS_TOOLS_BOOTSTRAP_PASSWORD"
            "where" "auth.bootstrapAdmins.passwordLogin" "field" "password"
            "value" (dig "auth" "bootstrapAdmins" "passwordLogin" "password" "" $c)
            "ref" (dig "auth" "bootstrapAdmins" "passwordLogin" "passwordSecret" dict $c))
      (dict "id" "ldapBindPassword" "env" "DEVOPS_TOOLS_LDAP_BIND_PASSWORD"
            "where" "auth.ldap" "field" "bindPassword"
            "value" (dig "auth" "ldap" "bindPassword" "" $c)
            "ref" (dig "auth" "ldap" "bindPasswordSecret" dict $c))
      (dict "id" "ldapCaCert" "env" "" "defaultKey" "ca.crt"
            "where" "auth.ldap.tls" "field" "caCert"
            "value" (dig "auth" "ldap" "tls" "caCert" "" $c)
            "ref" (dig "auth" "ldap" "tls" "caCertSecret" dict $c))
      (dict "id" "consulToken" "env" "CONSUL_HTTP_TOKEN"
            "where" "configurations.consul" "field" "token"
            "value" (dig "configurations" "consul" "token" "" $c)
            "ref" (dig "configurations" "consul" "tokenSecret" dict $c))
      (dict "id" "gitlabToken" "env" "GITLAB_TOKEN"
            "where" "configurations.git" "field" "token"
            "value" (dig "configurations" "git" "token" "" $c)
            "ref" (dig "configurations" "git" "tokenSecret" dict $c))
      (dict "id" "imageRebuildToken" "env" "DEVOPS_TOOLS_IMAGE_REBUILD_TOKEN"
            "where" "imageRebuild" "field" "token"
            "value" (dig "imageRebuild" "token" "" $c)
            "ref" (dig "imageRebuild" "tokenSecret" dict $c))
-}}
{{- $out := dict -}}
{{- range $s := $specs -}}
{{- $refName := "" -}}
{{- $refKey := "" -}}
{{- if kindIs "map" $s.ref -}}
{{- $refName = ($s.ref.name | default "") -}}
{{- $refKey = ($s.ref.key | default "") -}}
{{- end -}}
{{- if and $s.value (or (kindIs "slice" $s.value) (kindIs "map" $s.value)) -}}
{{- fail (printf "%s.%s must be the value itself, not a list or a map. Written as [] it looks harmless, because an empty one is indistinguishable from nothing set — but filled in the same shape it reaches the container with its brackets attached, and only whatever reads it says so." $s.where $s.field) -}}
{{- end -}}
{{- if and $s.value $refName -}}
{{- fail (printf "%s sets both %s and %sSecret.name. Supply the value, or name a Secret that holds it — not both: which one wins is nobody's intention, only an ordering." $s.where $s.field $s.field) -}}
{{- end -}}
{{- if or $s.value $refName -}}
{{- $key := $refKey | default (default $s.env $s.defaultKey) -}}
{{- $_ := set $out $s.id (dict "env" $s.env "inline" $s.value "secretName" $refName "secretKey" $key) -}}
{{- end -}}
{{- end -}}
{{- $out | toJson -}}
{{- end -}}

{{/*
Session signing key.

Generating a fresh key on every upgrade would sign every user out, so an
existing secret is looked up first and its key reused. A key given in values
always wins.
*/}}
{{- define "devops-tools.sessionKey" -}}
{{- if .Values.session.key -}}
{{- .Values.session.key -}}
{{- else -}}
{{- $secretName := include "devops-tools.fullname" . -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace $secretName -}}
{{- if and $existing (index $existing.data "sessionKey") -}}
{{- index $existing.data "sessionKey" | b64dec -}}
{{- else -}}
{{- randAlphaNum 48 -}}
{{- end -}}
{{- end -}}
{{- end -}}
