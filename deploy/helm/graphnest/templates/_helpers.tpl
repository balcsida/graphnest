{{- define "graphnest.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "graphnest.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{- $name := include "graphnest.name" . }}{{- if contains $name .Release.Name }}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}{{ end }}{{ end }}
{{- end }}
{{- define "graphnest.labels" -}}
helm.sh/chart: {{ include "graphnest.chart" . }}
app.kubernetes.io/name: {{ include "graphnest.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "graphnest.chart" -}}{{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}{{- end }}
{{- define "graphnest.selectorLabels" -}}
app.kubernetes.io/name: {{ include "graphnest.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "graphnest.image" -}}
{{- printf "%s@%s" (required "image repository is required" .repository) (required "image sha256 digest is required" .digest) -}}
{{- end }}
{{- define "graphnest.podSecurityContext" -}}
{{- $context := dict -}}
{{- range $key, $value := omit . "runAsNonRoot" "seccompProfile" -}}
{{- /* A null value clears a chart default, e.g. fsGroup on OpenShift restricted SCCs. */ -}}
{{- if not (kindIs "invalid" $value) }}{{ $_ := set $context $key $value }}{{ end -}}
{{- end -}}
{{- $_ := set $context "runAsNonRoot" true -}}
{{- $_ := set $context "seccompProfile" (dict "type" "RuntimeDefault") -}}
{{- toYaml $context -}}
{{- end }}
{{- define "graphnest.resourceName" -}}
{{- $base := include "graphnest.fullname" (index . 0) -}}
{{- $suffix := index . 1 -}}
{{- printf "%s-%s" ($base | trunc (int (sub 62 (len $suffix))) | trimSuffix "-") $suffix -}}
{{- end }}
{{- define "graphnest.serverName" -}}{{ include "graphnest.resourceName" (list . "server") }}{{- end }}
{{- define "graphnest.nodeName" -}}{{ include "graphnest.resourceName" (list . "node") }}{{- end }}
{{- define "graphnest.zoektName" -}}{{ include "graphnest.resourceName" (list . "zoekt") }}{{- end }}
{{- define "graphnest.indexerName" -}}{{ include "graphnest.resourceName" (list . "indexer") }}{{- end }}
