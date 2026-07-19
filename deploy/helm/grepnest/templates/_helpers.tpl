{{- define "grepnest.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "grepnest.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{- $name := include "grepnest.name" . }}{{- if contains $name .Release.Name }}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}{{ end }}{{ end }}
{{- end }}
{{- define "grepnest.labels" -}}
helm.sh/chart: {{ include "grepnest.chart" . }}
app.kubernetes.io/name: {{ include "grepnest.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "grepnest.chart" -}}{{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}{{- end }}
{{- define "grepnest.selectorLabels" -}}
app.kubernetes.io/name: {{ include "grepnest.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "grepnest.image" -}}
{{- printf "%s@%s" (required "image repository is required" .repository) (required "image sha256 digest is required" .digest) -}}
{{- end }}
{{- define "grepnest.podSecurityContext" -}}
{{- $context := omit . "runAsNonRoot" "seccompProfile" -}}
{{- $_ := set $context "runAsNonRoot" true -}}
{{- $_ := set $context "seccompProfile" (dict "type" "RuntimeDefault") -}}
{{- toYaml $context -}}
{{- end }}
{{- define "grepnest.resourceName" -}}
{{- $base := include "grepnest.fullname" (index . 0) -}}
{{- $suffix := index . 1 -}}
{{- printf "%s-%s" ($base | trunc (int (sub 62 (len $suffix))) | trimSuffix "-") $suffix -}}
{{- end }}
{{- define "grepnest.serverName" -}}{{ include "grepnest.resourceName" (list . "server") }}{{- end }}
{{- define "grepnest.nodeName" -}}{{ include "grepnest.resourceName" (list . "node") }}{{- end }}
{{- define "grepnest.zoektName" -}}{{ include "grepnest.resourceName" (list . "zoekt") }}{{- end }}
